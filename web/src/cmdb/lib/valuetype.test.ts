import { describe, expect, it } from 'vitest';

import { displayValue, inputTypeFor, isEmptyValue, toControlValue, toWireValue } from './valuetype';

describe('the form/wire boundary', () => {
  // AN EMPTY CONTROL IS `null`, NOT `''`. The backend stores no row for a missing
  // value but can match an empty string, so sending '' for a cleared field
  // creates data where the operator cleared it.
  it('sends null for a cleared field', () => {
    expect(toWireValue('2', '')).toBeNull();
    expect(toWireValue('0', '')).toBeNull();
    expect(toWireValue('7', '')).toBeNull();
  });

  it('coerces the types the backend validates', () => {
    expect(toWireValue('0', '8')).toBe(8);
    expect(toWireValue('0', '8.7')).toBe(8);
    expect(toWireValue('1', '8.5')).toBe(8.5);
    expect(toWireValue('7', 'true')).toBe(true);
    expect(toWireValue('7', 'false')).toBe(false);
    expect(toWireValue('2', 'web-01')).toBe('web-01');
  });

  it('normalises the browser datetime into the stored form', () => {
    // `datetime-local` gives `2026-10-17T09:30`; the backend's DATETIME rule
    // accepts a space and has no timezone suffix.
    expect(toWireValue('3', '2026-10-17T09:30')).toBe('2026-10-17 09:30');
    expect(toWireValue('3', '2026-10-17T09:30:00Z')).toBe('2026-10-17 09:30:00');
  });

  it('leaves a value it cannot read as text rather than turning it into NaN', () => {
    // A NUMBER INPUT CAN STILL HOLD SOMETHING ODD (a pasted unit, a bad locale).
    // Sending NaN would be a value the backend rejects with a message about a
    // number; sending the text lets its own validation say what is wrong.
    expect(toWireValue('0', 'eight')).toBe('eight');
  });
});

describe('the control side', () => {
  it('renders a stored value back into a control', () => {
    expect(toControlValue('0', 8)).toBe('8');
    expect(toControlValue('7', true)).toBe('true');
    expect(toControlValue('7', null)).toBe('false');
    expect(toControlValue('3', '2026-10-17 09:30:00')).toBe('2026-10-17T09:30');
    expect(toControlValue('2', null)).toBe('');
  });

  it('picks the browser-native input for each type', () => {
    expect(inputTypeFor('0')).toBe('number');
    expect(inputTypeFor('1')).toBe('number');
    expect(inputTypeFor('3')).toBe('datetime-local');
    expect(inputTypeFor('4')).toBe('date');
    expect(inputTypeFor('5')).toBe('time');
    expect(inputTypeFor('7')).toBe('checkbox');
    expect(inputTypeFor('2')).toBe('text');
    expect(inputTypeFor('6')).toBe('text');
  });
});

describe('display', () => {
  it('joins a multi-valued attribute into one cell', () => {
    // `is_list` is several rows in one value table; the console shows them as one
    // cell, the same reading that makes an `IN` filter a membership test.
    expect(displayValue(['a', 'b'])).toBe('a, b');
    expect(displayValue([])).toBe('');
    expect(displayValue(null)).toBe('');
    expect(displayValue(0)).toBe('0');
    expect(displayValue(false)).toBe('false');
  });

  it('distinguishes no value from an empty one', () => {
    expect(isEmptyValue(null)).toBe(true);
    expect(isEmptyValue(undefined)).toBe(true);
    expect(isEmptyValue([])).toBe(true);
    expect(isEmptyValue('')).toBe(true);
    // AN EMPTY STRING IS A STORED VALUE THE BACKEND CAN MATCH, but it is still
    // empty as far as a control is concerned — the two are different facts and
    // only the write path needs to tell them apart.
    expect(isEmptyValue(0)).toBe(false);
    expect(isEmptyValue(false)).toBe(false);
  });
});
