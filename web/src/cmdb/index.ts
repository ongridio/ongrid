// The CMDB module's public surface.
//
// THE HOST IMPORTS ONLY FROM HERE. Everything a page needs — the shared table,
// the drawer, the tree, the dynamic attribute control, the query builder, the
// icon resolver, the toast — is exported below, so the host never reaches into
// a CMDB file path and the module stays free to reorganise its internals.

export { cmdbRoutes, cmdbNavSection, cmdbPaletteRoutes, CMDB_HOME } from './routes';
export type { CMDBRoute, CMDBNavItem } from './routes';

export { DataTable } from './components/DataTable';
export type { Column, SortState } from './components/DataTable';
export { Drawer } from './components/Drawer';
export { Tree } from './components/Tree';
export type { TreeNode } from './components/Tree';
export { AttributeControl, AttributeValue } from './components/AttributeControl';
export { QueryBar } from './components/QueryBar';
export type { QueryAttribute } from './components/QueryBar';
export { CMDBIcon, preloadCMDBIcons } from './components/CMDBIcon';
export { ToastProvider, useToast } from './components/Toast';

export { describeError, cmdbDelete, cmdbGet, cmdbPost, cmdbPut, queryString } from './api/http';
export type { ListResponse, Query } from './api/http';

export * from './lib/dsl';
export * from './lib/iconvalue';
export * from './lib/valuetype';
