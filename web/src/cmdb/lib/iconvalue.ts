// A CI type's icon, as the data stores it.
//
// THE ICON IS DATA, NOT A COMPONENT NAME. `c_ci_types.icon` holds a string the
// console resolves at render time, which is why a model's icon can be chosen by
// an operator and why replacing the set with a fixed icon library would be a
// migration, not a refactor.
//
// THE STRING HAS FOUR PARTS, separated by `$$`:
//
//   type$$color$$isCustom$$url
//
//   type      the glyph's name in the icon font (`caise-jiaohuanji`)
//   color     an optional colour the operator picked, overriding the glyph's own
//   isCustom  `1` when the operator uploaded an image instead of picking a glyph
//   url       that image's location, for a custom icon
//
// A value may carry fewer than four parts — `caise-jiaohuanji$$` is what a
// picked glyph with no colour override looks like, and cantor's own picker
// writes exactly that. So the parser pads rather than rejects.

export type CMDBIconValue = {
  /** The glyph name, or '' when the value names none. */
  type: string;
  /** A `#rrggbb`-style override, or '' to keep the glyph's own colours. */
  color: string;
  /** True when the icon is an uploaded image rather than a font glyph. */
  custom: boolean;
  /** The uploaded image's URL, for a custom icon. */
  url: string;
};

const EMPTY: CMDBIconValue = { type: '', color: '', custom: false, url: '' };

/**
 * Parses a stored icon value.
 *
 * A MISSING VALUE IS NOT AN ERROR. Every CI type created before icons existed
 * (and every model that never set one) has an empty column, and a console that
 * threw on it would fail to draw a list the operator can see is fine.
 */
export function parseIconValue(raw: string | null | undefined): CMDBIconValue {
  if (!raw) return EMPTY;
  const parts = String(raw).split('$$');
  const color = (parts[1] ?? '').trim();
  const flag = (parts[2] ?? '').trim().toLowerCase();
  return {
    type: (parts[0] ?? '').trim(),
    color: isValidColor(color) ? color : '',
    custom: flag === '1' || flag === 'true',
    url: (parts[3] ?? '').trim(),
  };
}

/** Serialises an icon choice back into the stored form. */
export function formatIconValue(value: CMDBIconValue): string {
  return [value.type, value.color, value.custom ? '1' : '0', value.url].join('$$');
}

/**
 * Whether a colour override is a literal colour rather than a name.
 *
 * THE CHECK IS NARROW ON PURPOSE. The value goes into an SVG attribute, so
 * anything is accepted by the browser only if it is a colour it understands; a
 * permissive pass-through would let a stored string reach the DOM as an
 * attribute we did not validate. `#rgb`, `#rrggbb`, `#rrggbbaa` and the three
 * keywords the picker emits are all this console ever writes.
 */
export function isValidColor(raw: string): boolean {
  if (raw === '') return false;
  if (raw === 'currentColor' || raw === 'inherit' || raw === 'transparent') return true;
  return /^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$/.test(raw);
}
