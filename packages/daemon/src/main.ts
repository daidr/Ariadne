import { browserProfiles, createSandbox, detectSandbox } from '@ariadne/sandbox'
import { dirname, resolve } from 'node:path'
import { parseArgs } from 'node:util'
import { certificate, daemonHome, hasToken, newToken, serveDaemon } from './daemon'
import { DEFAULT_PORT, isLoopback, pairingCode, parseAddress } from './protocol'

const usage = `用法：
  ariadne-daemon [--listen 地址:端口] [--hide 路径]... [--no-sandbox]
  ariadne-daemon pair      换一个新的配对码，旧的立即失效

默认只监听 127.0.0.1:${DEFAULT_PORT}。`

const { values, positionals } = parseArgs({
  args: Bun.argv.slice(2),
  allowPositionals: true,
  options: {
    listen: { type: 'string', default: `127.0.0.1:${DEFAULT_PORT}` },
    hide: { type: 'string', multiple: true, default: [] },
    'no-sandbox': { type: 'boolean', default: false },
    help: { type: 'boolean', short: 'h', default: false },
  },
})

if (values.help || positionals.length > 1 || (positionals[0] && positionals[0] !== 'pair')) {
  console.info(usage)
  process.exit(values.help ? 0 : 2)
}
if (process.platform === 'win32') {
  console.error('daemon 暂不支持 Windows')
  process.exit(1)
}

const home = daemonHome()
const cert = await certificate(home)
const printCode = (token: string) =>
  console.info(`配对码（只显示这一次）：\n\n  ${pairingCode(token, cert.fingerprint)}\n`)

if (positionals[0] === 'pair') {
  printCode(newToken(home))
  process.exit(0)
}

const listen = parseAddress(values.listen)
if (!listen) {
  console.error(`监听地址不对：${values.listen}`)
  process.exit(2)
}

const sandbox = createSandbox({
  kind: values['no-sandbox'] ? null : await detectSandbox(),
  mode: values['no-sandbox'] ? 'off' : 'auto',
  hide: [home, ...browserProfiles(), ...values.hide],
})
// The daemon's own code: a thread that changed it would run as the daemon next time.
const code = process.execPath.endsWith('/bun')
  ? resolve(import.meta.dir, '../../..')
  : dirname(process.execPath)
const server = serveDaemon({
  home,
  hostname: listen.host,
  port: listen.port,
  certificate: cert,
  sandbox,
  readOnly: [code],
})

console.info(`ariadne-daemon 已启动：${listen.host}:${server.port}`)
if (!isLoopback(listen.host)) console.info('监听的不是本机回环地址，其他机器也能连上来')
if (!sandbox.kind) {
  console.warn(
    values['no-sandbox']
      ? '沙箱已关闭：thread 的命令能读到 daemon 的证书私钥'
      : '这台机器上没有可用的沙箱：thread 的命令能读到 daemon 的证书私钥',
  )
}
if (!hasToken(home)) printCode(newToken(home))
else console.info('已配对。要换配对码，运行 ariadne-daemon pair')
