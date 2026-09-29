// @vitest-environment jsdom
import { describe, it, expect, beforeEach } from 'vitest';
import { PROTOCOL_VERSION, protocolAction } from './protocol';

describe('protocolAction', () => {
  beforeEach(() => sessionStorage.clear());

  it('accepts the matching version', () => {
    expect(protocolAction(PROTOCOL_VERSION, sessionStorage)).toBe('accept');
  });

  it('reloads once on a mismatched version, then ignores it', () => {
    expect(protocolAction(PROTOCOL_VERSION + 1, sessionStorage)).toBe('reload');
    expect(protocolAction(PROTOCOL_VERSION + 1, sessionStorage)).toBe('ignore');
  });

  it('rejects the pre-swarm version 1 payload', () => {
    expect(protocolAction(1, sessionStorage)).toBe('reload');
  });

  it('re-arms the reload after a matching version is seen', () => {
    protocolAction(PROTOCOL_VERSION + 1, sessionStorage);
    protocolAction(PROTOCOL_VERSION, sessionStorage);
    expect(protocolAction(PROTOCOL_VERSION + 1, sessionStorage)).toBe('reload');
  });

  it('reloads without storage but never ignores', () => {
    expect(protocolAction(PROTOCOL_VERSION + 1, undefined)).toBe('reload');
  });
});
