import { describe, expect, it } from 'vitest';
import { parseImmichInput } from './immich-input';

describe('parseImmichInput', () => {
  it('treats a bare id/key as assetId', () => {
    expect(parseImmichInput('00000000-0000-4000-8000-000000000001')).toEqual({
      assetId: '00000000-0000-4000-8000-000000000001',
    });
  });

  it('treats a full share URL as shareLink', () => {
    expect(parseImmichInput('https://photos.example.com/share/fakeShareKeyAbc123')).toEqual({
      shareLink: 'https://photos.example.com/share/fakeShareKeyAbc123',
    });
  });

  it('recognises a bare /share/ path even without a scheme', () => {
    expect(parseImmichInput('photos.example.com/share/abc')).toEqual({ shareLink: 'photos.example.com/share/abc' });
  });

  it('trims surrounding whitespace', () => {
    expect(parseImmichInput('  vid-123  ')).toEqual({ assetId: 'vid-123' });
  });
});
