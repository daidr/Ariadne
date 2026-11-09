import { fileIconOf, MessageIcon } from '@dou-ui/icons'
import type { ChatInputOption } from 'dou-ui'
import { computed, ref, watch, type Ref } from 'vue'
import { api, type MentionInput } from '../api'
import { kindIcon } from '../artifacts/kinds'
import { t } from '../i18n'
import { state } from '../store'

/** The `@…` being typed just before the caret: what follows the `@`, or `null`. */
export function mentionQuery(text: string, caret: number): string | null {
  const match = /(?:^|\s)@(\S*)$/.exec(text.slice(0, caret))
  return match ? match[1]! : null
}

/** A title as it is typed after `@`: one word, so the text still reads as written. */
export const handleOf = (title: string) => title.replace(/\s+/g, '') || title

/**
 * What the input offers after `@`: the project's threads and artifacts, and the files the
 * conversation can read, asked for as the user types. What was picked and is still in the
 * text goes with the message.
 */
export function useMentions(
  draft: Ref<string>,
  caret: () => number,
  conversationId: () => string | null,
  /** What was picked so far; kept elsewhere when the input outlives this component. */
  picked: Map<string, MentionInput> = new Map(),
) {
  const files = ref<string[]>([])

  // One word per thread or artifact; a title used twice gets a short tail of its id.
  const named = computed(() => {
    const used = new Set<string>()
    const unique = (title: string, id: string) => {
      let value = handleOf(title)
      if (used.has(value)) value = `${value}~${id.slice(-4)}`
      used.add(value)
      return value
    }
    const self = conversationId()
    return [
      ...(state.detail?.threads ?? [])
        // Archived threads are out of the way here too; the thread list still has them.
        .filter((thread) => thread.id !== self && !thread.archived)
        .map((thread) => ({
          option: {
            value: unique(thread.title, thread.id),
            label: thread.title,
            description: t('mention.thread'),
            icon: MessageIcon,
          },
          mention: { kind: 'thread' as const, id: thread.id },
        })),
      ...state.artifacts.map((artifact) => ({
        option: {
          value: unique(artifact.title, artifact.id),
          label: artifact.title,
          description: t('mention.artifact'),
          icon: kindIcon[artifact.kind],
        },
        mention: { kind: 'artifact' as const, id: artifact.id },
      })),
    ]
  })

  const options = computed<ChatInputOption[]>(() => [
    ...named.value.map((item) => item.option),
    ...files.value.map((path) => {
      const slash = path.lastIndexOf('/')
      return {
        value: path,
        label: path.slice(slash + 1),
        ...(slash > 0 ? { description: path.slice(0, slash) } : {}),
        icon: fileIconOf(path.split('.').at(-1) ?? ''),
      }
    }),
  ])

  // Files are asked for as the query changes, a moment after typing stops.
  let timer: ReturnType<typeof setTimeout> | undefined
  let asked = ''
  watch(draft, (text) => {
    const query = mentionQuery(text, caret())
    const projectId = state.detail?.project.id
    const id = conversationId()
    clearTimeout(timer)
    if (query === null || !projectId || !id) return
    if (`${id}\0${query}` === asked) return
    timer = setTimeout(async () => {
      asked = `${id}\0${query}`
      try {
        const { files: found } = await api.mentionFiles(projectId, id, query)
        if (asked === `${id}\0${query}`) files.value = found
      } catch {
        files.value = []
      }
    }, 150)
  })

  function pick(option: ChatInputOption) {
    const item = named.value.find((entry) => entry.option.value === option.value)
    picked.set(option.value, item ? item.mention : { kind: 'file', path: option.value })
  }

  /** The picked mentions still in `text`, once each. */
  function inText(text: string): MentionInput[] {
    return [...picked.entries()]
      .filter(([value]) =>
        new RegExp(`(?:^|\\s)@${escape(value)}(?=$|[\\s,.;:!?，。；：！？、])`).test(text),
      )
      .map(([, mention]) => mention)
  }

  /** Once a message is sent, the next one starts with nothing picked. */
  const reset = () => picked.clear()

  return { options, pick, inText, reset }
}

const escape = (text: string) => text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
