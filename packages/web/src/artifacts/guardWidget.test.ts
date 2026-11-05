// @vitest-environment happy-dom
import { mount } from '@vue/test-utils'
import { computed, defineComponent, h, nextTick, ref } from 'vue'
import { describe, expect, test, vi } from 'vitest'

const props = ref<Record<string, unknown>>({ delta: 0.012 })
vi.mock('@dou-ui/dashboard', () => ({
  useDashboardWidget: () => ({ props: computed(() => props.value) }),
}))
vi.mock('./WidgetBroken.vue', () => ({
  default: defineComponent({ render: () => h('p', { class: 'broken' }, 'broken') }),
}))

const { guardWidget } = await import('./guardWidget')

/** Reads `delta` as a path, as the stat widget does: a number throws. */
const Stat = defineComponent({
  render: () => h('span', String((props.value.delta as string).split('.'))),
})

describe('guardWidget', () => {
  test('a widget that throws shows the notice in its card, and is tried again when its props change', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    const type = guardWidget({ type: 'stat', label: 'stat', component: Stat })
    const wrapper = mount(type.component)
    await nextTick()
    expect(wrapper.find('.broken').exists()).toBe(true)

    props.value = { delta: 'passDelta' }
    await nextTick()
    await nextTick()
    expect(wrapper.find('.broken').exists()).toBe(false)
    expect(wrapper.text()).toBe('passDelta')
  })
})
