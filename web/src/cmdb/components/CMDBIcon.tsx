import { useEffect, useState } from 'react';
import { Boxes } from 'lucide-react';

import { cn } from '@/lib/cn';
import { parseIconValue } from '../lib/iconvalue';

// A model's icon, resolved at render time from the string the data carries.
//
// THE ICON IS DATA AND CANNOT BECOME A COMPONENT NAME. `c_ci_types.icon` holds
// an iconfont glyph name an operator chose; replacing it with a fixed icon
// library would change what every configured model looks like. So the console
// keeps the indirection, exactly as cantor did — see lib/iconvalue.ts for the
// string's four parts.
//
// THE GLYPHS ARE FETCHED, NOT BUNDLED. They are 727 symbols and about 2 MiB of
// path data, and most pages draw none of them — so they live in
// `web/public/cmdb/` and are asked for once, on the first page that draws an
// icon. Bundling them (even as a lazy chunk) would put them through the
// type-checker and the bundler on every build for no benefit.
//
// THE FALLBACK IS A REAL ICON, NOT AN EMPTY BOX. An unknown name, a failed
// chunk, or a model with no icon all draw the same neutral glyph, so a table
// stays aligned and a blank square never appears where a model should be.

type IconSymbol = { v: string; c: string };
type IconSymbols = Record<string, IconSymbol>;

let symbols: IconSymbols | null = null;
let pending: Promise<IconSymbols> | null = null;
const listeners = new Set<() => void>();

const SPRITE_URL = '/cmdb/cmdb-icons.json';

function loadSymbols(): Promise<IconSymbols> {
  if (symbols) return Promise.resolve(symbols);
  if (!pending) {
    pending = fetch(SPRITE_URL, { credentials: 'same-origin' })
      .then((response) => {
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        return response.json() as Promise<IconSymbols>;
      })
      .then((loaded) => {
        symbols = loaded;
        listeners.forEach((listener) => listener());
        return symbols;
      })
      .catch(() => {
        // A MISSING SPRITE IS NOT A REASON TO BREAK A PAGE: the fallback is
        // already what an unresolved name draws. The failure is remembered so a
        // list of a thousand rows does not retry it a thousand times.
        symbols = {};
        listeners.forEach((listener) => listener());
        return symbols;
      });
  }
  return pending;
}

/** Loads the glyphs ahead of time; a test or a page may await it. */
export function preloadCMDBIcons(): Promise<IconSymbols> {
  return loadSymbols();
}

/** The glyph map, or null while it is loading. */
export function useCMDBIcons(): IconSymbols | null {
  const [loaded, setLoaded] = useState<IconSymbols | null>(symbols);
  useEffect(() => {
    if (symbols) {
      setLoaded(symbols);
      return;
    }
    let live = true;
    const sync = () => {
      if (live) setLoaded(symbols);
    };
    listeners.add(sync);
    void loadSymbols().then(sync);
    return () => {
      live = false;
      listeners.delete(sync);
    };
  }, []);
  return loaded;
}

type Props = {
  /** The stored `c_ci_types.icon` value. */
  value?: string | null;
  /** Square size in pixels; default 16, which is a table cell's icon. */
  size?: number;
  className?: string;
  /** An accessible name. Required, because a decorative model icon is rare. */
  label: string;
};

export function CMDBIcon({ value, size = 16, className, label }: Props) {
  const parsed = parseIconValue(value);
  const glyphs = useCMDBIcons();

  if (parsed.custom && parsed.url) {
    // AN UPLOADED IMAGE IS NOT A GLYPH. The value's fourth part is where the
    // operator's own picture lives, and it is drawn as an image rather than
    // looked up.
    return (
      <img
        src={parsed.url}
        alt={label}
        width={size}
        height={size}
        className={cn('shrink-0 rounded-sm object-contain', className)}
      />
    );
  }

  const symbol = parsed.type ? glyphs?.[parsed.type] : undefined;
  if (!symbol) {
    return (
      <Boxes
        size={size}
        aria-label={label}
        role="img"
        className={cn('shrink-0 text-text-faint', className)}
      />
    );
  }

  return (
    <svg
      width={size}
      height={size}
      viewBox={symbol.v}
      fill={parsed.color || 'currentColor'}
      role="img"
      aria-label={label}
      className={cn('shrink-0', className)}
      // THE MARKUP IS A BUILD-TIME CONSTANT, NOT OPERATOR INPUT: it comes from
      // the vendored icon font extracted by web/scripts/extract-cmdb-icons.py.
      // The only operator-controlled part is `parsed.color`, and iconvalue.ts
      // accepts it only when it is a literal colour.
      dangerouslySetInnerHTML={{ __html: symbol.c }}
    />
  );
}
