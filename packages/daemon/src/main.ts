import { createTranslator, terminalLocale } from '@ariadne/i18n'
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

if (values.stdio) {
  // Ariadne came in through ssh, which already decided who may connect: no token, no
  // certificate, and nothing but frames on standard output.
  await serveStdio()
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
  if (!hasToken(home)) printCode(newToken(home))
  else console.info(t('cli.daemon.paired'))
}
