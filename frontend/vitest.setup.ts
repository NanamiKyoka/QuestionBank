/**
 * 测试环境的全局补齐。
 *
 * Node 22 起 `globalThis.localStorage` 是个实验性实现，没开 `--localstorage-file`
 * 时读出来是 undefined；vitest 把 jsdom 的全局搬过来时会跳过**已经存在**的同名键，
 * 于是测试里拿到的是 Node 那个读不出东西的版本，而 jsdom 的实现被留在
 * `_localStorage` 上。store 在创建时就会碰 localStorage，不换回去的话
 * 所有用到 jsdom 的测试都会在第一行就炸。
 *
 * 非 jsdom 环境（默认的 node 环境）没有这两个键，直接跳过。
 */
const globals = globalThis as unknown as Record<string, unknown>

function restoreStorage(implKey: string, publicName: string) {
  const impl = globals[implKey]
  if (typeof impl !== 'object' || impl === null) return
  Object.defineProperty(globalThis, publicName, {
    value: impl,
    configurable: true,
    writable: true,
    enumerable: true,
  })
}

restoreStorage('_localStorage', 'localStorage')
restoreStorage('_sessionStorage', 'sessionStorage')
