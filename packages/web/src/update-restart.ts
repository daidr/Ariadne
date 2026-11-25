import type { RestartPhase, UpdateStatus } from './api'
import { t } from './i18n'

export function effectiveRestartPhase(status: UpdateStatus | null | undefined): RestartPhase {
  if (!status) return ''
  if (status.restartPhase) return status.restartPhase
  if (status.restarting) {
    return (status.activeTurns ?? 0) > 0 ? 'draining' : 'restarting'
  }
  return ''
}

export function isUpdateRestartable(status: UpdateStatus | null | undefined): boolean {
  return status?.state === 'ready' && Boolean(status.canRestart)
}

export function hasUpdateIndicator(status: UpdateStatus | null | undefined): boolean {
  return effectiveRestartPhase(status) !== '' || isUpdateRestartable(status)
}

export function updateIndicatorTone(
  status: UpdateStatus | null | undefined,
): 'primary' | 'warning' {
  const phase = effectiveRestartPhase(status)
  return phase === 'draining' || phase === 'restarting' ? 'warning' : 'primary'
}

export function updateIndicatorLabel(status: UpdateStatus | null | undefined): string {
  const phase = effectiveRestartPhase(status)
  if (phase === 'restarting') return t('updates.header.restarting')
  if (phase === 'draining') return t('updates.header.draining')
  if (phase === 'waiting_idle') return t('updates.header.waitingIdle')
  return t('updates.header.ready')
}

export function hasActiveRestartWork(status: UpdateStatus | null | undefined): boolean {
  if (!status) return false
  return (status.activeTurns ?? 0) > 0 || (status.activeBackground ?? 0) > 0
}

export function restartStatusMessage(status: UpdateStatus | null | undefined): string | null {
  if (!status) return null
  const phase = effectiveRestartPhase(status)
  const turns = status.activeTurns ?? 0
  const background = status.activeBackground ?? 0
  if (phase === 'waiting_idle') {
    if (turns > 0 && background > 0) {
      return t('updates.waitingIdleBoth', { turns, background })
    }
    if (turns > 0) {
      return t('updates.waitingIdleTurns', { turns })
    }
    if (background > 0) {
      return t('updates.waitingIdleBackground', { background })
    }
    return t('updates.waitingIdle')
  }
  if (phase === 'draining') {
    if (turns > 0) {
      return t('updates.restartingDraining', { count: turns })
    }
    return t('updates.restartingDrainingIdle')
  }
  if (phase === 'restarting') {
    return t('updates.restarting')
  }
  return null
}

export function restartNowWarningMessage(status: UpdateStatus | null | undefined): string | null {
  if (!status) return null
  const turns = status.activeTurns ?? 0
  const background = status.activeBackground ?? 0
  if (turns > 0 && background > 0) {
    return t('updates.restartNowHintBoth', { turns, background })
  }
  if (turns > 0) {
    return t('updates.restartNowHintTurns', { turns })
  }
  if (background > 0) {
    return t('updates.restartNowHintBackground', { background })
  }
  return null
}
