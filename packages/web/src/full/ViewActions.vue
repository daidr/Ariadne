<script setup lang="ts">
import { DouButton, DouTooltip } from 'dou-ui'
import { CloseIcon, MaximizeIcon, PanelRightIcon } from '@dou-ui/icons'
import { t } from '../i18n'
import { closeFull, fullScreenPane, fullToSide, paneKeyOf, type OpenedView } from '../store'

/**
 * The end of an artifact's or changes' header: full screen from a tab on the right side;
 * back to the side, or closed, from full screen.
 */
const props = defineProps<{ view: OpenedView; place: 'side' | 'full' }>()

const actions = () =>
  props.place === 'side'
    ? [
        {
          label: t('view.full'),
          icon: MaximizeIcon,
          run: () => fullScreenPane(paneKeyOf(props.view)),
        },
      ]
    : [
        { label: t('view.toSide'), icon: PanelRightIcon, run: fullToSide },
        { label: t('view.close'), icon: CloseIcon, run: closeFull },
      ]
</script>

<template>
  <div class="view-actions">
    <DouTooltip
      v-for="action in actions()"
      :key="action.label"
      :content="action.label"
      placement="bottom"
    >
      <DouButton size="sm" variant="ghost" :aria-label="action.label" @click="action.run">
        <component :is="action.icon" />
      </DouButton>
    </DouTooltip>
  </div>
</template>

<style scoped>
.view-actions {
  display: flex;
  flex: none;
  gap: 2px;
}
</style>
