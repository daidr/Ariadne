// @vitest-environment happy-dom
import { mount } from '@vue/test-utils'
import { describe, expect, test } from 'vitest'
import { nextTick } from 'vue'
import MessageImage from '../markdown/MessageImage.vue'

describe('MessageImage', () => {
  test('opens native DouImagePreview when clicking a loaded image and closes on Escape', async () => {
    const wrapper = mount(MessageImage, {
      props: {
        src: 'https://example.com/photo.png',
        alt: 'Photo',
      },
      attachTo: document.body,
    })

    try {
      await wrapper.get('img').trigger('load')
      await nextTick()
      expect(wrapper.get('.dou-image').classes()).toContain('is-previewable')

      await wrapper.get('.dou-image').trigger('click')
      await nextTick()
      const dialog = document.body.querySelector<HTMLElement>('.dou-image-preview')
      expect(dialog).not.toBeNull()
      expect(dialog?.style.display).not.toBe('none')

      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
      await nextTick()
      expect(dialog?.style.display).toBe('none')
    } finally {
      wrapper.unmount()
    }
  })
})
