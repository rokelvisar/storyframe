// Real-browser end-to-end test of the client pipeline: file pick ->
// <video>/<canvas> macro collage -> AudioContext/MediaRecorder audio ->
// tus-js-client segment uploads (interesting-first) -> timeline UI -> job complete.
//
// This exists because the API-level tests (scripts/e2e.sh) bypass the browser
// entirely and missed two shipping bugs: frame-extractor hanging on
// requestVideoFrameCallback for a paused <video>, and the VLM overview clobbering
// a user's explicit interest pick.
//
//   APP=http://localhost:8080 node frontend/e2e/browser-pipeline.mjs
//
// Requires: ffmpeg on PATH (builds a VP9/Opus WebM fixture the open-source
// Chromium can decode) and `npx playwright install chromium`. Runs headed under
// xvfb-run on headless hosts — the bundled headless *shell* can't decode video.
import { chromium } from "playwright";
import { execFileSync } from "node:child_process";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const APP = process.env.APP || "http://localhost:8080";
const DURATION = Number(process.env.DURATION || 95); // long enough for >1 segment

const work = mkdtempSync(join(tmpdir(), "vx-e2e-"));
const fixture = join(work, "fixture.webm");
console.log(`building ${DURATION}s fixture -> ${fixture}`);
execFileSync("ffmpeg", [
  "-v", "error", "-y",
  "-f", "lavfi", "-i", `testsrc2=s=640x360:r=24:d=${DURATION}`,
  "-f", "lavfi", "-i", `sine=f=330:d=${DURATION}`,
  "-map", "0:v", "-map", "1:a",
  "-c:v", "libvpx-vp9", "-b:v", "900k", "-pix_fmt", "yuv420p",
  "-deadline", "good", "-cpu-used", "6", "-row-mt", "1", "-g", "48",
  "-c:a", "libopus", "-b:a", "48k", "-f", "webm", fixture,
], { stdio: "inherit" });

const log = (...a) => console.log(new Date().toISOString().slice(11, 19), ...a);
const consoleErrs = [], pageErrs = [], failed = [];
const browser = await chromium.launch({
  headless: false, // full Chromium: the headless shell lacks video decoders
  args: ["--no-sandbox", "--autoplay-policy=no-user-gesture-required"],
});
const page = await browser.newPage({ viewport: { width: 1280, height: 1000 } });
page.on("console", (m) => m.type() === "error" && consoleErrs.push(m.text()));
page.on("pageerror", (e) => pageErrs.push(String(e)));
page.on("requestfailed", (r) => { if (!r.url().startsWith("blob:")) failed.push(`${r.method()} ${r.url()} :: ${r.failure()?.errorText || ""}`); });

let jobId = null;
const seen = new Set();
page.on("request", (r) => {
  const u = r.url().replace(APP, "");
  if (/\/api\/v1\/jobs$/.test(u) && r.method() === "POST") seen.add("POST /jobs");
  if (/\/overview$/.test(u)) seen.add("POST /overview");
  if (/\/audio$/.test(u)) seen.add("POST /audio");
  if (u === "/files/" && r.method() === "POST") seen.add("tus POST");
  if (/^\/files\/\w+/.test(u) && r.method() === "PATCH") seen.add("tus PATCH");
  if (/\/segments\/.+\/process$/.test(u)) seen.add("POST /process");
  if (/\/chat$/.test(u)) seen.add("POST /chat");
});
page.on("response", async (r) => {
  if (r.url().endsWith("/api/v1/jobs") && r.request().method() === "POST") {
    try { jobId = (await r.json())?.job?.id; log("job:", jobId); } catch {}
  }
});

const phase = () => page.locator(".phasechip").first().textContent().then((t) => (t || "").trim()).catch(() => "?");
const jobJson = () => page.request.get(`${APP}/api/v1/jobs/${jobId}`).then((r) => r.json());

let failMsg = "";
try {
  await page.goto(APP, { waitUntil: "domcontentloaded" });
  await page.waitForSelector("vx-root input[type=file]", { timeout: 15000 });
  await page.setInputFiles("input[type=file]", fixture);

  await page.waitForSelector("vx-segment", { timeout: 90000 });
  await page.waitForTimeout(1500);
  const n = await page.locator("vx-segment").count();
  log("segment cards:", n);
  if (n < 2) failMsg += `expected >=2 segments, got ${n}. `;

  // click the last (latest, least interesting) card -> mark interesting
  await page.locator("vx-segment").nth(n - 1).click();
  log("clicked last segment");
  await page.waitForTimeout(2000);

  const deadline = Date.now() + 300000;
  let last = "";
  while (Date.now() < deadline) {
    const p = await phase();
    if (p !== last) { log("phase:", p); last = p; }
    if (p === "complete" || p === "error") break;
    await page.waitForTimeout(2000);
  }
  if (last !== "complete") failMsg += `pipeline ended in phase "${last}". `;

  // Let the async overview + audio settle. Audio extraction plays the whole
  // clip back in real time (MediaRecorder), so the job can reach "complete"
  // (segments done, archive assembled) well before audioReceived flips —
  // give it the fixture's own duration plus a generous margin, not a fixed
  // guess, or this flakes on a slower CI runner / longer DURATION.
  const audioDeadline = Date.now() + DURATION * 1000 + 60000;
  let lastJob = null;
  while (Date.now() < audioDeadline) {
    lastJob = await jobJson();
    if (lastJob.overviewStory && lastJob.audioReceived) break;
    await page.waitForTimeout(2000);
  }
  const api = lastJob ?? (await jobJson());

  // Chat: ask a question through the real UI and wait for a reply bubble.
  // scripts/e2e.sh never touches the browser, so this is the only check that
  // exercises vx-chat at all (per the frame-extractor/interest-pick bugs this
  // suite already caught, an API-only check here wouldn't be enough).
  await page.locator("vx-chat input[type=text]").fill("What happens in this video?");
  await page.locator("vx-chat input[type=text]").press("Enter");
  await page.waitForSelector("vx-chat .msg.assistant", { timeout: 30000 }).catch(() => {});
  const chatReplyCount = await page.locator("vx-chat .msg.assistant").count();
  const chatAfter = await jobJson();

  const eventLogEntryCount = await page.locator("vx-event-log .entry").count();

  const checks = {
    "job complete": api.status === "complete",
    "provider present": !!api.analysisProvider,
    "20 frames from browser collage": (api.frames || []).length === 20,
    "overview story non-empty": (api.overviewStory || "").length > 40,
    "audio received": api.audioReceived === true,
    "every segment done": api.segments.every((s) => s.status === "done"),
    "every segment has a granular collage": api.segments.every((s) => !!s.granularCollageUrl),
    "every segment has a description": api.segments.every((s) => (s.description || "").length > 20),
    "archive assembled": !!api.archiveUrl,
    "process() was called": seen.has("POST /process"),
    "user pick recorded + not clobbered by VLM":
      api.segments.some((s) => s.source === "user" && s.interestScore >= 0.9),
    "tus upload used": seen.has("tus POST") && seen.has("tus PATCH"),
    "chat question sent": seen.has("POST /chat"),
    "chat reply rendered in the UI": chatReplyCount >= 1,
    "chat reply persisted server-side": (chatAfter.chatMessages || []).length === 2,
    "activity log rendered in the UI": eventLogEntryCount > 0,
    "activity log mentions the analysis provider": (chatAfter.events || []).some((e) =>
      e.message.includes(`provider=${api.analysisProvider}`)),
    "activity log records job completion": (chatAfter.events || []).some((e) => e.message === "job complete"),
  };

  console.log("\n================ CHECKS ================");
  for (const [k, v] of Object.entries(checks)) {
    console.log(`  ${v ? "PASS" : "FAIL"}  ${k}`);
    if (!v) failMsg += `check failed: ${k}. `;
  }
  console.log("seen:", [...seen].sort());
  console.log("seg sources :", JSON.stringify(api.segments.map((s) => s.source)));
  console.log("seg interest:", JSON.stringify(api.segments.map((s) => +s.interestScore.toFixed(2))));
  console.log("story       :", (api.overviewStory || "").slice(0, 140));
} finally {
  if (consoleErrs.length) { console.log("console errors:", consoleErrs); failMsg += "console errors present. "; }
  if (pageErrs.length) { console.log("page errors:", pageErrs); failMsg += "page errors present. "; }
  if (failed.length) { console.log("failed requests:", failed); failMsg += "non-blob request failures. "; }
  await browser.close();
}

if (failMsg) { console.error("\nFAIL:", failMsg); process.exit(1); }
console.log("\nPASS — full client pipeline verified in a real browser.");
