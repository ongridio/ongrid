import '@testing-library/jest-dom';
import { afterAll, afterEach, beforeAll } from 'vitest';
import { server } from './msw-server';

// Node 22+ 自带的实验性 webstorage 全局会盖掉 jsdom 的 localStorage——
// 没有 --localstorage-file 时它的 getItem 是 undefined，导致任何
// 走 useI18n()/getLocale() 的组件测试在 render 时抛
// "localStorage.getItem is not a function"。这里换成内存实现兜底。
if (typeof globalThis.localStorage?.getItem !== 'function') {
  const mem = new Map<string, string>();
  const stub: Storage = {
    get length() {
      return mem.size;
    },
    clear: () => mem.clear(),
    getItem: (k: string) => mem.get(k) ?? null,
    key: (i: number) => [...mem.keys()][i] ?? null,
    removeItem: (k: string) => void mem.delete(k),
    setItem: (k: string, v: string) => void mem.set(k, String(v)),
  };
  Object.defineProperty(globalThis, 'localStorage', { value: stub, configurable: true });
}

// onUnhandledRequest:'error' so any test forgetting a handler fails
// loudly instead of silently hanging on a real fetch.
beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => server.resetHandlers());
afterAll(() => server.close());
