import { builtinEnvironments } from 'vitest/environments';
import type { Environment } from 'vitest';

// Node fetch / Request 校验原生 AbortSignal；JSDOM 的同名对象不满足
// Node 25 的类型检查。在安装 DOM 前保留配套对象，仅用于测试环境。
export default {
  name: 'fetch-jsdom',
  transformMode: 'web',
  async setup(global, options) {
    const { AbortController, AbortSignal } = global;
    const environment = await builtinEnvironments.jsdom.setup(global, options);
    global.AbortController = AbortController;
    global.AbortSignal = AbortSignal;
    return environment;
  },
} satisfies Environment;
