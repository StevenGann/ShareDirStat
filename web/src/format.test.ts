import { describe, expect, it } from 'vitest';
import { bytesPerMinute, formatBytesPerMin, formatPlaytime } from './format';

describe('formatPlaytime', () => {
  it('renders like a media player', () => {
    expect(formatPlaytime(undefined)).toBe('—');
    expect(formatPlaytime(0)).toBe('—');
    expect(formatPlaytime(45)).toBe('0:45');
    expect(formatPlaytime(245)).toBe('4:05');
    expect(formatPlaytime(3600)).toBe('1:00:00');
    expect(formatPlaytime(7112)).toBe('1:58:32');
  });

  it('switches to days for the very long aggregates', () => {
    expect(formatPlaytime(300000)).toBe('3d 11h');
  });
});

describe('bytesPerMinute', () => {
  it('is media bytes over playing minutes', () => {
    expect(bytesPerMinute({ duration: 60, media_size: 100 })).toBe(100);
    expect(bytesPerMinute({ duration: 30, media_size: 100 })).toBe(200);
  });

  it('is undefined without media', () => {
    expect(bytesPerMinute({})).toBeUndefined();
    expect(bytesPerMinute({ duration: 0, media_size: 100 })).toBeUndefined();
    expect(bytesPerMinute({ duration: 60 })).toBeUndefined();
  });
});

describe('formatBytesPerMin', () => {
  it('reuses the byte formatter with a rate suffix', () => {
    expect(formatBytesPerMin(1024 * 1024)).toBe('1.00 MiB/min');
    expect(formatBytesPerMin(undefined)).toBe('—');
  });
});
