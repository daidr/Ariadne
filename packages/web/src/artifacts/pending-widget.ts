import { PENDING } from '@ariadne/core'
import { defineDashboardWidget } from '@dou-ui/dashboard'
import { t } from '../i18n'
import PendingWidget from './PendingWidget.vue'

/** A placeholder in a dashboard: what an agent will put in this tile, written in grey. */
export const pendingWidget = () =>
  defineDashboardWidget<{ text?: string }>({
    type: PENDING,
    label: t('artifact.pending'),
    size: { w: 6, h: 3, minW: 2, minH: 1 },
    defaultProps: { text: '' },
    component: PendingWidget,
    fullscreen: false,
  })
