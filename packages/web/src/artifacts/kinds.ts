import type { ArtifactKind } from '@ariadne/core'
import { DashboardIcon, FileTextIcon, ShapesIcon } from '@dou-ui/icons'
import type { Component } from 'vue'

export const ARTIFACT_KINDS: ArtifactKind[] = ['document', 'whiteboard', 'dashboard']

export const kindIcon: Record<ArtifactKind, Component> = {
  document: FileTextIcon,
  whiteboard: ShapesIcon,
  dashboard: DashboardIcon,
}
