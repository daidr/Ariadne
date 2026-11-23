<script setup lang="ts" generic="T extends { id: string }">
import { DouChatList, DouSpinner, type ChatListExpose } from 'dou-ui'
import { useTemplateRef } from 'vue'

defineProps<{
  items: T[]
  loadingEarlier: boolean
  bottomInset?: number | string
  bottomFade?: boolean
}>()
const emit = defineEmits<{
  bottomChange: [atBottom: boolean]
  earlier: []
}>()
defineSlots<{ default: (props: { item: T }) => unknown }>()

const list = useTemplateRef<ChatListExpose>('list')
defineExpose({
  get viewport() {
    return list.value?.viewport ?? null
  },
  scrollToMessage(id: string) {
    list.value?.scrollToMessage(id)
  },
})
</script>

<template>
  <div class="conversation-log">
    <div v-if="loadingEarlier" class="earlier" role="status">
      <DouSpinner size="sm" />
    </div>
    <DouChatList
      ref="list"
      class="messages"
      :items="items"
      item-key="id"
      :bottom-inset="bottomInset"
      :bottom-fade="bottomFade"
      @at-bottom-change="emit('bottomChange', $event)"
      @top-reached="emit('earlier')"
    >
      <template #item="{ item }"><slot :item="item" /></template>
    </DouChatList>
  </div>
</template>

<style scoped>
.conversation-log {
  position: relative;
  display: flex;
  flex: 1;
  min-height: 0;
}

.messages {
  flex: 1;
  min-width: 0;
}

.earlier {
  position: absolute;
  inset-block-start: 4px;
  inset-inline: 0;
  z-index: 1;
  display: flex;
  justify-content: center;
  pointer-events: none;
}
</style>
