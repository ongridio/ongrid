import type { ReactNode } from 'react';
import { X } from 'lucide-react';

import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/Dialog';
import { useI18n } from '@/i18n/locale';
import { cn } from '@/lib/cn';

// The right-hand detail panel.
//
// A CMDB IS READ SIDEWAYS. Opening an instance, a model or a subnet should not
// take the list away: an operator compares two hosts, checks a rack while a
// filter is still set, or moves down a subnet list one address at a time. cantor
// used a sheet for exactly this, and the drawer is the one interaction the
// design language names as the CMDB's primary detail surface.
//
// IT IS THE SHARED DIALOG, NOT A SECOND OVERLAY. `DialogContent` already has a
// `placement="right"` variant wired to `.og-dialog-content[data-placement="right"]`,
// which gives a full-height panel, the shared backdrop, Escape handling and focus
// return. Re-implementing a sheet here would put a second overlay implementation
// in the tree with its own focus bugs.

const WIDTHS = {
  md: 'w-[min(28rem,100vw)]',
  lg: 'w-[min(44rem,100vw)]',
  xl: 'w-[min(64rem,100vw)]',
} as const;

type Props = {
  open: boolean;
  onClose: () => void;
  title: ReactNode;
  /** A one-line explanation under the title. */
  description?: ReactNode;
  /** Sticky footer, typically the save / cancel pair. */
  footer?: ReactNode;
  size?: keyof typeof WIDTHS;
  children: ReactNode;
};

export function Drawer({ open, onClose, title, description, footer, size = 'md', children }: Props) {
  const { tr } = useI18n();

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
    >
      <DialogContent placement="right" className={cn('max-w-none gap-0', WIDTHS[size])}>
        <header className="flex items-start justify-between gap-3 border-b border-border px-5 py-4">
          <div className="min-w-0">
            <DialogTitle className="truncate text-sm font-semibold text-text">{title}</DialogTitle>
            {description && (
              <DialogDescription className="mt-0.5 text-xs text-text-muted">{description}</DialogDescription>
            )}
          </div>
          {/* A NAMED CLOSE BUTTON. An icon-only control still needs an
              accessible name, and Escape alone is not discoverable. */}
          <button
            type="button"
            onClick={onClose}
            aria-label={tr('关闭', 'Close')}
            className="shrink-0 rounded p-1 text-text-faint hover:text-text focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-indigo-500"
          >
            <X size={14} aria-hidden="true" />
          </button>
        </header>

        {/* THE BODY SCROLLS, THE HEADER AND FOOTER DO NOT — so the close button
            and the save button stay reachable however long the panel gets. */}
        <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">{children}</div>

        {footer && <footer className="border-t border-border px-5 py-3">{footer}</footer>}
      </DialogContent>
    </Dialog>
  );
}
