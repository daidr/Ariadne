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
