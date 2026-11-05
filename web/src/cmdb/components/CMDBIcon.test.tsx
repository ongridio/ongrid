import { render, screen, waitFor } from '@testing-library/react';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it } from 'vitest';

import { server } from '@/test/msw-server';
import { CMDBIcon, preloadCMDBIcons } from './CMDBIcon';

// The resolver: a name from the data, a glyph from the sprite, a fallback when
// either is missing.
//
// ONE TEST RATHER THAN FOUR, because the sprite is loaded once per module and
// cached — splitting the assertions would make them depend on which file ran
// first. The sequence below is the real one: a page draws an icon before the
// sprite has arrived, then again after.

const SPRITE = {
  'caise-switch': { v: '0 0 1024 1024', c: '<path d="M0 0h10v10H0z"/>' },
};

beforeEach(() => {
  localStorage.setItem('ongrid-locale', 'zh-CN');
});

describe('resolving a stored icon', () => {
  it('falls back, then draws the glyph, then honours an uploaded image', async () => {
    server.use(http.get('/cmdb/cmdb-icons.json', () => HttpResponse.json(SPRITE)));

    const { rerender } = render(<CMDBIcon value="caise-switch$$" label="switch" />);
    // BEFORE THE SPRITE ARRIVES, OR WHEN THE NAME IS NOT IN IT: a neutral glyph
    // with the model's name, so the row stays aligned and the icon is never a
    // blank square.
    expect(screen.getByRole('img', { name: 'switch' }).tagName.toLowerCase()).toBe('svg');
    // The fallback is a real glyph of the console's own icon set, so the way to
    // tell it from the sprite's is that it is NOT the sprite's path.
    expect(screen.getByRole('img', { name: 'switch' }).innerHTML).not.toContain('M0 0h10v10H0z');

    await preloadCMDBIcons();

    rerender(<CMDBIcon value="caise-switch$$" label="switch" />);
    await waitFor(() => expect(screen.getByRole('img', { name: 'switch' }).innerHTML).toContain('<path'));

    // AN UNKNOWN NAME IS THE FALLBACK, not an empty box.
    rerender(<CMDBIcon value="no-such-glyph$$" label="unknown" />);
    expect(screen.getByRole('img', { name: 'unknown' }).innerHTML).not.toContain('M0 0h10v10H0z');

    // A CUSTOM UPLOADED IMAGE IS AN IMG, not a glyph lookup.
    rerender(<CMDBIcon value="$$$$1$$https://example.test/model.png" label="custom model" />);
    const image = screen.getByRole('img', { name: 'custom model' });
    expect(image.tagName.toLowerCase()).toBe('img');
    expect(image).toHaveAttribute('src', 'https://example.test/model.png');

    // A MODEL WITH NO ICON AT ALL still draws something labelled.
    rerender(<CMDBIcon value={null} label="bare" />);
    expect(screen.getByRole('img', { name: 'bare' })).toBeInTheDocument();
  });

  it('applies a stored colour override only when it is a colour', async () => {
    server.use(http.get('/cmdb/cmdb-icons.json', () => HttpResponse.json(SPRITE)));
    await preloadCMDBIcons();

    const { rerender } = render(<CMDBIcon value="caise-switch$$#ff0000$$0$$" label="red" />);
    expect(screen.getByRole('img', { name: 'red' })).toHaveAttribute('fill', '#ff0000');

    // THE OVERRIDE REACHES AN SVG ATTRIBUTE, which is why the parser refuses
    // anything that is not a literal colour and the glyph keeps its own.
    rerender(<CMDBIcon value="caise-switch$$url(javascript:alert(1))$$0$$" label="injected" />);
    expect(screen.getByRole('img', { name: 'injected' })).toHaveAttribute('fill', 'currentColor');
  });
});
