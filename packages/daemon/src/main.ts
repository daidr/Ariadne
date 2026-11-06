import { createTranslator, terminalLocale } from '@ariadne/i18n'
import { browserProfiles, createSandbox, detectSandbox } from '@ariadne/sandbox'
import { homedir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { parseArgs } from 'node:util'
import {
  certificate,
  daemonHome,
  hasToken,
  MissingOpensslError,
  newToken,
  serveDaemon,
  serveStdio,
} from './daemon'
import { DEFAULT_PORT, isLoopback, pairingCode, parseAddress } from './protocol'

// In the terminal's language.
const t = createTranslator(terminalLocale(process.env))

const { values, positionals } = parseArgs({
  args: Bun.argv.slice(2),
  allowPositionals: true,
  options: {
    listen: { type: 'string', default: `127.0.0.1:${DEFAULT_PORT}` },
    hide: { type: 'string', multiple: true, default: [] },
    'no-sandbox': { type: 'boolean', default: false },
    stdio: { type: 'boolean', default: false },
    help: { type: 'boolean', short: 'h', default: false },
  },
})

if (values.help || positionals.length > 1 || (positionals[0] && positionals[0] !== 'pair')) {
  console.info(t('cli.daemon.usage', { port: DEFAULT_PORT }))
  process.exit(values.help ? 0 : 2)
}
if (process.platform === 'win32') {
  console.error(t('cli.daemon.windows'))
  process.exit(1)
}

const home = daemonHome()

const sandbox = createSandbox({
  kind: values['no-sandbox'] ? null : await detectSandbox(),
  mode: values['no-sandbox'] ? 'off' : 'auto',
  // Ariadne's own home too, should it also run on this machine.
  hide: [home, join(homedir(), '.ariadne'), ...browserProfiles(), ...values.hide],
})
// The daemon's own code: a thread that changed it would run as the daemon next time.
const code = process.execPath.endsWith('/bun')
  ? resolve(import.meta.dir, '../../..')
  : dirname(process.execPath)

if (values.stdio) {
  // Ariadne came in through ssh, which already decided who may connect: no token, no
  // certificate, and nothing but frames on standard output.
  await serveStdio({ sandbox, readOnly: [code] })
  // Commands get a moment to end on their own before the process goes.
  setTimeout(() => process.exit(0), 2500)
} else {
  await serve()
}

async function serve() {
  const cert = await certificate(home).catch((error: unknown) => {
    console.error(
      error instanceof MissingOpensslError
        ? t('cli.daemon.noOpenssl')
        : t('cli.daemon.certificateFailed', {
            reason: error instanceof Error ? error.message : String(error),
          }),
    )
    process.exit(1)
  })
  const printCode = (token: string) =>
    console.info(t('cli.daemon.pairingCode', { code: pairingCode(token, cert.fingerprint) }))

  if (positionals[0] === 'pair') {
    printCode(newToken(home))
    process.exit(0)
  }

  const listen = parseAddress(values.listen)
  if (!listen) {
    console.error(t('cli.daemon.badListen', { address: values.listen }))
    process.exit(2)
  }

  let server: ReturnType<typeof serveDaemon>
  try {
    server = serveDaemon({
      home,
      hostname: listen.host,
      port: listen.port,
      certificate: cert,
      sandbox,
      readOnly: [code],
    })
  } catch (error) {
    console.error(
      t('cli.daemon.listenFailed', {
        address: values.listen,
        reason: error instanceof Error ? error.message : String(error),
      }),
    )
    process.exit(1)
  }

  console.info(t('cli.daemon.started', { address: `${listen.host}:${server.port}` }))
  if (!isLoopback(listen.host)) console.info(t('cli.daemon.notLoopback'))
  if (!sandbox.kind) {
    console.warn(t(values['no-sandbox'] ? 'cli.daemon.sandboxOff' : 'cli.daemon.noSandbox'))
  }
  if (!hasToken(home)) printCode(newToken(home))
  else console.info(t('cli.daemon.paired'))
}
