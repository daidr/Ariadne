// @vitest-environment happy-dom
import { mount } from '@vue/test-utils'
import { describe, expect, test, vi } from 'vitest'
import { nextTick } from 'vue'
import ImagePreview from './ImagePreview.vue'
import MessageImage from '../markdown/MessageImage.vue'

function mockStageLayout(
  stage: HTMLElement,
  img: HTMLImageElement,
  size = { width: 400, height: 400 },
) {
  vi.spyOn(stage, 'getBoundingClientRect').mockReturnValue(
    new DOMRect(0, 0, size.width, size.height),
  )
  Object.defineProperty(img, 'offsetWidth', { configurable: true, value: size.width })
  Object.defineProperty(img, 'offsetHeight', { configurable: true, value: size.height })
  Object.defineProperty(img, 'offsetLeft', { configurable: true, value: 0 })
  Object.defineProperty(img, 'offsetTop', { configurable: true, value: 0 })
  Object.defineProperty(img, 'offsetParent', { configurable: true, value: stage })
  vi.spyOn(img, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, size.width, size.height))
}

function pointerEvent(
  type: string,
  init: {
    pointerId: number
    clientX: number
    clientY: number
    pointerType?: string
    button?: number
  },
) {
  return new PointerEvent(type, {
    bubbles: true,
    cancelable: true,
    pointerId: init.pointerId,
    clientX: init.clientX,
    clientY: init.clientY,
    pointerType: init.pointerType ?? 'touch',
    button: init.button ?? 0,
  })
}

function parseTransform(style: string) {
  const match = /translate3d\(([-\d.]+)px,\s*([-\d.]+)px,\s*0(?:px)?\)\s*scale\(([-\d.]+)\)/.exec(
    style,
  )
  if (!match) throw new Error(`Unexpected transform: ${style}`)
  return {
    x: Number(match[1]),
    y: Number(match[2]),
    scale: Number(match[3]),
  }
}

describe('ImagePreview', () => {
  test('supports two-finger pinch zoom around midpoint and seamless handoff to pan', async () => {
    const wrapper = mount(ImagePreview, {
      props: {
        open: true,
        images: ['https://example.com/a.png'],
        teleport: false,
      },
      attachTo: document.body,
    })

    try {
      const stage = wrapper.get<HTMLElement>('.image-preview__stage').element
      const img = wrapper.get<HTMLImageElement>('.image-preview__image').element
      mockStageLayout(stage, img)

      // Start two fingers 100px apart centered at (250, 200) — one on stage, one on image.
      stage.dispatchEvent(pointerEvent('pointerdown', { pointerId: 1, clientX: 200, clientY: 200 }))
      img.dispatchEvent(pointerEvent('pointerdown', { pointerId: 2, clientX: 300, clientY: 200 }))
      await nextTick()
      expect(wrapper.get('.image-preview__image').classes()).toContain('is-interacting')

      // Spread fingers to 200px apart around the same midpoint (250, 200) -> 2x zoom anchored at (250, 200).
      window.dispatchEvent(
        pointerEvent('pointermove', { pointerId: 1, clientX: 150, clientY: 200 }),
      )
      window.dispatchEvent(
        pointerEvent('pointermove', { pointerId: 2, clientX: 350, clientY: 200 }),
      )
      await nextTick()

      expect(wrapper.get('.image-preview__scale').text()).toBe('200%')
      expect(parseTransform(img.style.transform)).toEqual({ x: -50, y: 0, scale: 2 })

      // Lift second finger -> remaining first finger seamlessly pans right by 40px.
      window.dispatchEvent(pointerEvent('pointerup', { pointerId: 2, clientX: 350, clientY: 200 }))
      window.dispatchEvent(
        pointerEvent('pointermove', { pointerId: 1, clientX: 190, clientY: 220 }),
      )
      await nextTick()
      expect(parseTransform(img.style.transform)).toEqual({ x: -10, y: 20, scale: 2 })

      // Pan far beyond edge -> clamped to [-200, 200].
      window.dispatchEvent(
        pointerEvent('pointermove', { pointerId: 1, clientX: 800, clientY: -500 }),
      )
      await nextTick()
      expect(parseTransform(img.style.transform)).toEqual({ x: 200, y: -200, scale: 2 })

      // Lift remaining finger -> interaction ends and modal stays open (pinch never triggers mask close).
      window.dispatchEvent(pointerEvent('pointerup', { pointerId: 1, clientX: 800, clientY: -500 }))
      await nextTick()
      expect(wrapper.get('.image-preview__image').classes()).not.toContain('is-interacting')
      expect(wrapper.emitted('close')).toBeUndefined()
    } finally {
      wrapper.unmount()
    }
  })

  test('clamps pinch zoom between 1x and maxScale', async () => {
    const wrapper = mount(ImagePreview, {
      props: {
        open: true,
        images: ['https://example.com/a.png'],
        maxScale: 4,
        teleport: false,
      },
      attachTo: document.body,
    })

    try {
      const stage = wrapper.get<HTMLElement>('.image-preview__stage').element
      const img = wrapper.get<HTMLImageElement>('.image-preview__image').element
      mockStageLayout(stage, img)

      // Pinch out aggressively -> capped at maxScale (4x).
      img.dispatchEvent(pointerEvent('pointerdown', { pointerId: 1, clientX: 180, clientY: 200 }))
      img.dispatchEvent(pointerEvent('pointerdown', { pointerId: 2, clientX: 220, clientY: 200 }))
      window.dispatchEvent(pointerEvent('pointermove', { pointerId: 1, clientX: 0, clientY: 200 }))
      window.dispatchEvent(
        pointerEvent('pointermove', { pointerId: 2, clientX: 400, clientY: 200 }),
      )
      await nextTick()
      expect(wrapper.get('.image-preview__scale').text()).toBe('400%')

      // Pinch inward past original distance -> stops cleanly at 100% centered.
      window.dispatchEvent(
        pointerEvent('pointermove', { pointerId: 1, clientX: 195, clientY: 200 }),
      )
      window.dispatchEvent(
        pointerEvent('pointermove', { pointerId: 2, clientX: 205, clientY: 200 }),
      )
      await nextTick()
      expect(wrapper.get('.image-preview__scale').text()).toBe('100%')
      expect(parseTransform(img.style.transform)).toEqual({ x: 0, y: 0, scale: 1 })

      window.dispatchEvent(pointerEvent('pointerup', { pointerId: 1, clientX: 195, clientY: 200 }))
      window.dispatchEvent(pointerEvent('pointerup', { pointerId: 2, clientX: 205, clientY: 200 }))
    } finally {
      wrapper.unmount()
    }
  })

  test('supports touch double-tap zoom/reset and avoids double-firing with synthetic dblclick', async () => {
    const wrapper = mount(ImagePreview, {
      props: {
        open: true,
        images: ['https://example.com/a.png'],
        teleport: false,
      },
      attachTo: document.body,
    })

    try {
      const stage = wrapper.get<HTMLElement>('.image-preview__stage').element
      const img = wrapper.get<HTMLImageElement>('.image-preview__image').element
      mockStageLayout(stage, img)

      // First touch tap on image.
      img.dispatchEvent(pointerEvent('pointerdown', { pointerId: 10, clientX: 300, clientY: 200 }))
      window.dispatchEvent(pointerEvent('pointerup', { pointerId: 10, clientX: 300, clientY: 200 }))
      await nextTick()
      expect(wrapper.get('.image-preview__scale').text()).toBe('100%')

      // Second quick touch tap at the same spot -> zooms to 200% anchored at (300, 200).
      img.dispatchEvent(pointerEvent('pointerdown', { pointerId: 11, clientX: 302, clientY: 201 }))
      window.dispatchEvent(pointerEvent('pointerup', { pointerId: 11, clientX: 302, clientY: 201 }))
      await nextTick()
      expect(wrapper.get('.image-preview__scale').text()).toBe('200%')
      expect(parseTransform(img.style.transform)).toEqual({ x: -102, y: -1, scale: 2 })

      // A browser-synthesized dblclick immediately after touch double-tap must not reset it back.
      img.dispatchEvent(new MouseEvent('dblclick', { bubbles: true, clientX: 302, clientY: 201 }))
      await nextTick()
      expect(wrapper.get('.image-preview__scale').text()).toBe('200%')

      // Another touch double-tap resets back to 100% centered.
      img.dispatchEvent(pointerEvent('pointerdown', { pointerId: 12, clientX: 200, clientY: 200 }))
      window.dispatchEvent(pointerEvent('pointerup', { pointerId: 12, clientX: 200, clientY: 200 }))
      img.dispatchEvent(pointerEvent('pointerdown', { pointerId: 13, clientX: 200, clientY: 200 }))
      window.dispatchEvent(pointerEvent('pointerup', { pointerId: 13, clientX: 200, clientY: 200 }))
      await nextTick()
      expect(wrapper.get('.image-preview__scale').text()).toBe('100%')
      expect(parseTransform(img.style.transform)).toEqual({ x: 0, y: 0, scale: 1 })
    } finally {
      wrapper.unmount()
    }
  })

  test('preserves desktop wheel zoom, dblclick, mask close, and Escape close', async () => {
    const wrapper = mount(ImagePreview, {
      props: {
        open: true,
        images: ['https://example.com/a.png', 'https://example.com/b.png'],
        teleport: false,
      },
      attachTo: document.body,
    })

    try {
      const stage = wrapper.get<HTMLElement>('.image-preview__stage').element
      const img = wrapper.get<HTMLImageElement>('.image-preview__image').element
      mockStageLayout(stage, img)

      // Wheel zoom in.
      stage.dispatchEvent(
        new WheelEvent('wheel', {
          bubbles: true,
          cancelable: true,
          deltaY: -100,
          clientX: 200,
          clientY: 200,
        }),
      )
      await nextTick()
      expect(wrapper.get('.image-preview__scale').text()).toBe('125%')

      // Desktop mouse dblclick zooms to 200% and resets back to 100%.
      img.dispatchEvent(new MouseEvent('dblclick', { bubbles: true, clientX: 200, clientY: 200 }))
      await nextTick()
      expect(wrapper.get('.image-preview__scale').text()).toBe('100%')

      // Trackpad pinch (ctrl+wheel) zooms continuously.
      const pinchWheel = new WheelEvent('wheel', {
        bubbles: true,
        cancelable: true,
        deltaY: -50,
        clientX: 200,
        clientY: 200,
      })
      Object.defineProperty(pinchWheel, 'ctrlKey', { value: true })
      stage.dispatchEvent(pinchWheel)
      await nextTick()
      expect(parseTransform(img.style.transform).scale).toBeGreaterThan(1.5)

      // Switching image with ArrowRight resets zoom back to 100%.
      await wrapper.get('.image-preview').trigger('keydown', { key: 'ArrowRight' })
      await nextTick()
      expect(wrapper.emitted('update:index')?.[0]).toEqual([1])
      await wrapper.setProps({ index: 1 })
      await nextTick()
      expect(wrapper.get('.image-preview__scale').text()).toBe('100%')

      // Dragging at 1x does not pan the image.
      const nextImg = wrapper.get<HTMLImageElement>('.image-preview__image').element
      mockStageLayout(stage, nextImg)
      nextImg.dispatchEvent(
        pointerEvent('pointerdown', { pointerId: 19, clientX: 200, clientY: 200 }),
      )
      window.dispatchEvent(
        pointerEvent('pointermove', { pointerId: 19, clientX: 280, clientY: 260 }),
      )
      window.dispatchEvent(pointerEvent('pointerup', { pointerId: 19, clientX: 280, clientY: 260 }))
      await nextTick()
      expect(parseTransform(nextImg.style.transform)).toEqual({ x: 0, y: 0, scale: 1 })

      // Single tap/click on image does not close.
      nextImg.dispatchEvent(
        pointerEvent('pointerdown', {
          pointerId: 20,
          pointerType: 'mouse',
          clientX: 200,
          clientY: 200,
        }),
      )
      window.dispatchEvent(
        pointerEvent('pointerup', {
          pointerId: 20,
          pointerType: 'mouse',
          clientX: 200,
          clientY: 200,
        }),
      )
      await nextTick()
      expect(wrapper.emitted('close')).toBeUndefined()

      // Tap on empty mask closes preview once.
      stage.dispatchEvent(
        pointerEvent('pointerdown', {
          pointerId: 21,
          pointerType: 'touch',
          clientX: 10,
          clientY: 10,
        }),
      )
      window.dispatchEvent(
        pointerEvent('pointerup', {
          pointerId: 21,
          pointerType: 'touch',
          clientX: 10,
          clientY: 10,
        }),
      )
      stage.dispatchEvent(new MouseEvent('click', { bubbles: true, clientX: 10, clientY: 10 }))
      await nextTick()
      expect(wrapper.emitted('close')).toHaveLength(1)
      expect(wrapper.emitted('update:open')?.[0]).toEqual([false])

      // Reopen and verify Escape closes via layer stack.
      await wrapper.setProps({ open: false })
      await nextTick()
      await wrapper.setProps({ open: true })
      await nextTick()
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
      await nextTick()
      expect(wrapper.emitted('close')).toHaveLength(2)
    } finally {
      wrapper.unmount()
    }
  })

  test('opens ImagePreview when clicking a loaded MessageImage', async () => {
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
      expect(wrapper.get('.message-image').classes()).toContain('is-previewable')

      await wrapper.get('.message-image').trigger('click')
      await nextTick()
      expect(document.body.querySelector('.image-preview__stage')).not.toBeNull()
    } finally {
      wrapper.unmount()
    }
  })
})
