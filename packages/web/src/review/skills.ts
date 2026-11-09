import { BookTextIcon } from '@dou-ui/icons'
import type { ChatInputOption } from 'dou-ui'
import { computed, onScopeDispose, ref, watch } from 'vue'
import { api, type MentionInput, type SkillView } from '../api'
import { onLiveEvent, state } from '../store'

const SKILL = 'skill:'

/**
 * The skills a conversation can be asked to work by, offered after `/` in its input:
 * picking one writes `/name` in the text, and a name still there goes with the message.
 */
export function useSkills(
  conversationId: () => string | null,
  /** The names picked so far; kept elsewhere when the input outlives this component. */
  picked: Set<string> = new Set(),
) {
  const skills = ref<SkillView[]>([])

  async function read(id: string) {
    try {
      const { skills: found } = await api.getSkills(id)
      if (conversationId() === id) skills.value = found
    } catch {
      // No skills to offer; snippets still are.
    }
  }

  // Read again when the conversation changes, and when the project's source does.
  watch(
    // Each on its own: a refresh that leaves both as they were does not read again.
    [conversationId, () => state.detail?.project.source] as const,
    ([id]) => {
      skills.value = []
      if (id) void read(id)
    },
    { immediate: true },
  )
  // A turn may have written a skill; the ones offered stay until the new list is here.
  onScopeDispose(
    onLiveEvent((event) => {
      if (event.type === 'turn.finished' && event.data.conversationId === conversationId())
        void read(event.data.conversationId)
    }),
  )

  const options = computed<ChatInputOption[]>(() =>
    skills.value.map((skill) => ({
      value: `${SKILL}${skill.name}`,
      label: skill.name,
      ...(skill.when ? { description: skill.when } : {}),
      icon: BookTextIcon,
    })),
  )

  /** The text a picked skill puts in the input, or `null` when the option is not a skill. */
  function pick(option: ChatInputOption): string | null {
    if (!String(option.value).startsWith(SKILL)) return null
    const name = String(option.value).slice(SKILL.length)
    picked.add(name)
    return `/${name}`
  }

  /** The picked skills whose names are still in `text`. */
  function inText(text: string): MentionInput[] {
    return [...picked]
      .filter((name) => new RegExp(`(?:^|\\s)/${escape(name)}(?=$|\\s)`).test(text))
      .map((name) => ({ kind: 'skill' as const, name }))
  }

  const reset = () => picked.clear()

  return { options, pick, inText, reset }
}

const escape = (text: string) => text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
