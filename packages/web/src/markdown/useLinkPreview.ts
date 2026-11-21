import type { DouLink } from 'dou-ui'
import { shallowRef, watch, type Ref, type ShallowRef } from 'vue'
import type { LinkPreview } from '@ariadne/plugin-sdk'
import { linkPreview } from './link-summaries'

export function useLinkPreview(
  href: () => string,
  anchor: Readonly<ShallowRef<InstanceType<typeof DouLink> | null>>,
  open: Readonly<Ref<boolean>>,
  eager: () => boolean = () => true,
) {
  const preview = shallowRef<LinkPreview | null>()

  function load() {
    const address = href()
    void linkPreview(address).then((value) => {
      if (address === href()) preview.value = value
    })
  }

  watch(href, () => {
    preview.value = undefined
    if (open.value) load()
  })
  watch(open, (visible) => {
    if (visible) load()
  })
  watch(
    [anchor, href, eager],
    ([link, , shouldLoad], _before, cleanup) => {
      const element = link?.$el
      if (
        !shouldLoad ||
        typeof IntersectionObserver === 'undefined' ||
        !(element instanceof Element)
      )
        return
      const observer = new IntersectionObserver((entries) => {
        if (entries.some((entry) => entry.isIntersecting)) {
          observer.disconnect()
          load()
        }
      })
      observer.observe(element)
      cleanup(() => observer.disconnect())
    },
    { flush: 'post', immediate: true },
  )
  return preview
}
