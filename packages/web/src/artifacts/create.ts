import type { ContextMenuItem } from 'dou-ui'
import { computed } from 'vue'
import { t } from '../i18n'
import { createArtifact } from '../store'
import { ARTIFACT_KINDS, kindIcon } from './kinds'

/** The "new" menu: one empty artifact per kind, opened once made. */
export const createItems = computed<ContextMenuItem[]>(() =>
  ARTIFACT_KINDS.map((kind) => ({
    key: kind,
    label: t(`artifact.kind.${kind}`),
    icon: kindIcon[kind],
    run: () => void createArtifact(kind),
  })),
)
