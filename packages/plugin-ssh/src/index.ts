import { definePlugin, type ContextProvider } from '@ariadne/plugin-sdk'
import { connectRemote, type RemoteShell } from '@ariadne/remote'
import { posix } from 'node:path'
import { SshShell } from './transport'

export { sshOptions, SshShell } from './transport'

export interface SshSettings {
  host: string
  port?: number
  path: string
}

/** The "SSH" context source; shared connections keep their sockets under `dataDir`. */
export function sshProvider(
  dataDir: string,
  makeShell: (settings: { host: string; port?: number }) => RemoteShell = (target) =>
    new SshShell(target, posix.join(dataDir, 'cm')),
): ContextProvider<SshSettings> {
  return {
    kind: 'ssh',
    title: { 'zh-CN': 'SSH 远程目录', en: 'SSH remote directory' },
    remote: true,
    summary: '{host}:{path}',
    settings: {
      type: 'object',
      properties: {
        host: {
          type: 'string',
          title: { 'zh-CN': '主机', en: 'Host' },
          description: {
            'zh-CN':
              'user@host 或 ~/.ssh/config 里的 Host。使用你自己的密钥和 ssh-agent，不支持密码登录；第一次连接前先在终端里 ssh 一次，确认主机指纹',
            en: 'user@host or a Host from ~/.ssh/config. Uses your own keys and ssh-agent; passwords are not supported. Connect once in a terminal first to accept the host key',
          },
        },
        port: { type: 'number', title: { 'zh-CN': '端口', en: 'Port' } },
        path: {
          type: 'string',
          title: { 'zh-CN': '远端目录', en: 'Remote directory' },
          description: {
            'zh-CN': '绝对路径。git 仓库会在远端为每个讨论串建一个独立的 worktree',
            en: 'An absolute path. A git repository gets a separate worktree per thread on the remote machine',
          },
        },
      },
      required: ['host', 'path'],
    },
    provides: ['workspace'],
    async connect(settings) {
      const host = settings.host?.trim()
      const path = settings.path?.trim()
      // Anything starting with `-` would be read as an option of ssh.
      if (!host || host.startsWith('-') || /\s/.test(host)) throw new Error('the host is not valid')
      if (!path || !posix.isAbsolute(path))
        throw new Error('the directory must be an absolute path')
      const port = settings.port ? Number(settings.port) : undefined
      if (port !== undefined && !(Number.isInteger(port) && port > 0 && port < 65536)) {
        throw new Error('the port is not valid')
      }
      const shell = makeShell({ host, ...(port ? { port } : {}) })
      return { workspace: await connectRemote(shell, path) }
    },
  }
}

export default definePlugin({
  id: 'ssh',
  name: { 'zh-CN': 'SSH 远程目录', en: 'SSH remote directory' },
  setup(ctx) {
    return ctx.registerContextProvider(sshProvider(ctx.dataDir) as ContextProvider<never>)
  },
})
