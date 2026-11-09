import { describe, expect, it } from 'vitest';

import { formatIconValue, isValidColor, parseIconValue } from './iconvalue';

describe('a stored icon value', () => {
  it('reads the four parts', () => {
    expect(parseIconValue('caise-jiaohuanji$$#ff0000$$0$$')).toEqual({
      type: 'caise-jiaohuanji',
      color: '#ff0000',
      custom: false,
      url: '',
    });
  });

  it('pads a short value rather than rejecting it', () => {
    // `caise-jiaohuanji$$` IS WHAT THE PICKER WRITES for a glyph with no colour
    // override. A parser that demanded four parts would blank every icon in the
    // estate.
    expect(parseIconValue('caise-jiaohuanji$$')).toMatchObject({ type: 'caise-jiaohuanji', color: '', custom: false });
    expect(parseIconValue('caise-jiaohuanji')).toMatchObject({ type: 'caise-jiaohuanji', custom: false });
  });

  it('treats a missing value as "no icon" rather than an error', () => {
    expect(parseIconValue(null)).toEqual({ type: '', color: '', custom: false, url: '' });
    expect(parseIconValue('')).toEqual({ type: '', color: '', custom: false, url: '' });
  });

  it('recognises a custom uploaded image', () => {
    const parsed = parseIconValue('$$$$1$$https://example.test/a.png');
    expect(parsed.custom).toBe(true);
    expect(parsed.url).toBe('https://example.test/a.png');
    // The flag is written as `1` by cantor's picker; `true` is accepted too
    // because a hand-edited row may hold it.
    expect(parseIconValue('x$$$$true$$u').custom).toBe(true);
  });

  it('drops a colour override that is not a colour', () => {
    // THE COLOUR REACHES AN SVG ATTRIBUTE. A permissive pass-through would let a
    // stored string become an attribute the console did not validate; the check
    // is what keeps operator-controlled text out of the DOM.
    expect(parseIconValue('a$$url(javascript:alert(1))$$0$$').color).toBe('');
    expect(parseIconValue('a$$not-a-colour$$0$$').color).toBe('');
    expect(parseIconValue('a$$  #ABC  $$0$$').color).toBe('#ABC');
  });

  it('round-trips through the stored form', () => {
    const value = { type: 'caise-switch', color: '#00ff00', custom: false, url: '' };
    expect(formatIconValue(value)).toBe('caise-switch$$#00ff00$$0$$');
    expect(parseIconValue(formatIconValue(value))).toEqual(value);
  });
});

describe('colour validation', () => {
  it('accepts the literals the picker writes', () => {
    for (const raw of ['#abc', '#aabbcc', '#aabbccdd', 'currentColor', 'inherit', 'transparent']) {
      expect(isValidColor(raw), raw).toBe(true);
    }
  });

  it('refuses everything else, including a colour-looking injection', () => {
    for (const raw of ['', ' ', 'red', '#ab', '#abcde', 'url(x)', '#abc;x']) {
      expect(isValidColor(raw), raw).toBe(false);
    }
  });
});
