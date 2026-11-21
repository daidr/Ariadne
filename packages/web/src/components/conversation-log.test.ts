// @vitest-environment happy-dom
import { mount } from '@vue/test-utils'
import type { FormBlock } from '@ariadne/core'
import { expect, test } from 'vitest'
import { h, nextTick } from 'vue'
import AskBlock from '../blocks/AskBlock.vue'
import { askDraftsKey, type AskDraft } from '../blocks/ask-drafts'
import ConversationLog from './ConversationLog.vue'

test('mounts only nearby messages, jumps to a hidden message, and anchors history prepends', async () => {
  const items = Array.from({ length: 500 }, (_, index) => ({ id: `message-${index}` }))
  const wrapper = mount(ConversationLog, {
    props: { items, loadingEarlier: false },
    attachTo: document.body,
    slots: {
      default: ({ item }: { item: { id: string } }) => h('span', { 'data-id': item.id }, item.id),
    },
  })
  try {
    const viewport = wrapper.get<HTMLElement>('[role="log"]')
    Object.defineProperty(viewport.element, 'scrollHeight', { value: 100_000 })
    expect(wrapper.findAll('[data-id]').length).toBeLessThan(20)
    expect(wrapper.find('[data-id="message-250"]').exists()).toBe(false)

    wrapper.vm.scrollToMessage('message-250')
    await nextTick()
    await viewport.trigger('scroll')
    expect(wrapper.find('[data-id="message-250"]').exists()).toBe(true)
    expect(wrapper.find('[data-id="message-0"]').exists()).toBe(false)
    const top = viewport.element.scrollTop

    await wrapper.setProps({
      items: [{ id: 'older-message' }, ...items],
      loadingEarlier: true,
    })
    expect(wrapper.find('[data-id="message-250"]').exists()).toBe(true)
    expect(viewport.element.scrollTop).toBeGreaterThan(top)
    expect(wrapper.find('[role="status"]').exists()).toBe(true)
  } finally {
    wrapper.unmount()
  }
})

test('keeps an unfinished answer when its question scrolls out of the list', async () => {
  const block: FormBlock = {
    type: 'form',
    title: 'A question',
    description: null,
    submitText: null,
    fields: [
      {
        key: 'response',
        label: 'Response',
        kind: 'text',
        required: true,
        placeholder: null,
        options: [],
        default: '',
        min: null,
        max: null,
      },
    ],
  }
  const items = Array.from({ length: 500 }, (_, index) => ({ id: `message-${index}` }))
  const wrapper = mount(ConversationLog, {
    props: { items, loadingEarlier: false },
    attachTo: document.body,
    global: { provide: { [askDraftsKey as symbol]: new Map<string, AskDraft>() } },
    slots: {
      default: ({ item }: { item: { id: string } }) =>
        item.id === 'message-0'
          ? h(AskBlock, { attentionId: 'question', block, answer: null })
          : h('span', item.id),
    },
  })
  try {
    await wrapper.get('input').setValue('unfinished answer')
    wrapper.vm.scrollToMessage('message-250')
    await nextTick()
    expect(wrapper.find('input').exists()).toBe(false)
    wrapper.vm.scrollToMessage('message-0')
    await nextTick()
    expect(wrapper.get('input').element.value).toBe('unfinished answer')
  } finally {
    wrapper.unmount()
  }
})
