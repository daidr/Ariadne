import type { Attachment } from '@ariadne/core'
import type { ChatAttachment } from 'dou-ui'
import { onBeforeUnmount, ref } from 'vue'
import { api } from '../api'
import { report } from '../store'

/** A file in the input, while it uploads and once it is stored. */
export interface Draft extends ChatAttachment {
  fileId?: string
}

/** What models take at most, as `MAX_MODEL_IMAGE_BYTES` in core says. */
const MAX_MODEL_IMAGE_BYTES = 5 * 1024 * 1024
/** Larger pictures are drawn again smaller, at most this long a side. */
const MAX_SIDE = 2560

/**
 * A picture too large for models, drawn again smaller (JPEG) until it fits. Animated GIFs
 * and pictures the browser cannot draw are sent as they are; the model is told it could
 * not look at them.
 */
async function fitForModel(file: File): Promise<Blob> {
  if (
    !file.type.startsWith('image/') ||
    file.type === 'image/gif' ||
    file.size <= MAX_MODEL_IMAGE_BYTES
  ) {
    return file
  }
  let bitmap: ImageBitmap
  try {
    bitmap = await createImageBitmap(file)
  } catch {
    return file
  }
  try {
    for (const [side, quality] of [
      [MAX_SIDE, 0.9],
      [MAX_SIDE, 0.75],
      [1600, 0.75],
    ] as const) {
      const scale = Math.min(1, side / Math.max(bitmap.width, bitmap.height))
      const canvas = new OffscreenCanvas(
        Math.round(bitmap.width * scale),
        Math.round(bitmap.height * scale),
      )
      canvas.getContext('2d')?.drawImage(bitmap, 0, 0, canvas.width, canvas.height)
      const blob = await canvas.convertToBlob({ type: 'image/jpeg', quality })
      if (blob.size <= MAX_MODEL_IMAGE_BYTES) return blob
    }
    return file
  } finally {
    bitmap.close()
  }
}

/** Let go of a picked picture's preview. */
const release = (file: ChatAttachment) => {
  if (file.url?.startsWith('blob:')) URL.revokeObjectURL(file.url)
}

/** Files added to a message before it is sent: each uploads as soon as it is added. */
export function useAttachments(projectId: () => string | null) {
  const files = ref<Draft[]>([])
  let next = 0

  async function upload(draft: Draft, file: File) {
    const project = projectId()
    if (!project) return
    try {
      const blob = await fitForModel(file)
      const { file: stored } = await api.uploadFile(project, blob, true)
      const entry = files.value.find((item) => item.uid === draft.uid)
      if (!entry) return
      entry.fileId = stored.id
      entry.type = stored.mime
      entry.size = stored.size
      delete entry.status
    } catch (error) {
      // Not kept: the reason (say, a type that cannot be sent) is shown instead.
      const index = files.value.findIndex((item) => item.uid === draft.uid)
      if (index >= 0) release(files.value.splice(index, 1)[0]!)
      report(error)
    }
  }

  function add(list: Iterable<File>) {
    for (const file of list) {
      const draft: Draft = {
        uid: `a${next++}`,
        name: file.name || 'file',
        size: file.size,
        type: file.type,
        status: 'running',
        ...(file.type.startsWith('image/') ? { url: URL.createObjectURL(file) } : {}),
      }
      files.value.push(draft)
      void upload(draft, file)
    }
  }

  /** Every file is stored: the message can be sent with them. */
  const ready = () => files.value.every((file) => !!file.fileId)

  /** The files for a message being sent, out of the list until `settle` says how it went. */
  function take() {
    const taken = files.value
    files.value = []
    return {
      attachments: taken.flatMap((file) =>
        file.fileId ? [{ fileId: file.fileId, name: file.name }] : [],
      ),
      /** Sent: let go of their previews. Not sent: back in the list for another try. */
      settle(sent: boolean) {
        if (sent) taken.forEach(release)
        else files.value = [...taken, ...files.value]
      },
    }
  }

  onBeforeUnmount(() => files.value.forEach(release))
  return { files, add, ready, take, release }
}

/** A sent message's files, as the attachment list shows them. */
export function shownAttachments(attachments: Attachment[]): ChatAttachment[] {
  return attachments.map((file) => ({
    uid: file.fileId,
    name: file.name,
    size: file.size,
    type: file.mime,
    url: `/api/files/${file.fileId}`,
  }))
}
