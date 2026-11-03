import { json, optionalInt, optionalStr, str, ToolInputError, type Tool } from './tool-helpers'

const createDocument: Tool = {
  spec: {
    name: 'create_document',
    description:
      'Create a document in the project, written in Markdown: a plan, spec, report or notes the user will read and may edit. Long results belong in a document rather than a message; say in one line that it exists.',
    parameters: {
      type: 'object',
      properties: {
        title: { type: 'string' },
        content: { type: 'string', description: 'Markdown.' },
        nodeId: { type: 'string', description: 'The plan node the document belongs to, if any.' },
      },
      required: ['title', 'content'],
    },
  },
  run(args, ctx) {
    const nodeId = optionalStr(args, 'nodeId')
    const artifact = ctx.app.createArtifact(ctx.projectId, {
      kind: 'document',
      title: str(args, 'title'),
      content: str(args, 'content'),
      nodeId: nodeId || null,
      author: ctx.conversationId,
    })
    return json({ artifactId: artifact.id, version: artifact.version })
  },
}

const updateDocument: Tool = {
  spec: {
    name: 'update_document',
    description:
      "Save a new version of a document with its full Markdown content. baseVersion is the version you read: if anyone saved since, the call fails and you read it again, so the user's edits are never lost.",
    parameters: {
      type: 'object',
      properties: {
        artifactId: { type: 'string' },
        content: { type: 'string', description: 'The whole document, in Markdown.' },
        reason: { type: 'string', description: 'What changed, in one line.' },
        title: { type: 'string' },
        baseVersion: { type: 'integer' },
      },
      required: ['artifactId', 'content', 'reason', 'baseVersion'],
    },
  },
  run(args, ctx) {
    const artifactId = str(args, 'artifactId')
    if (ctx.app.getArtifact(artifactId).projectId !== ctx.projectId) {
      return { content: `No document ${artifactId} in this project.`, isError: true }
    }
    const baseVersion = optionalInt(args, 'baseVersion')
    if (baseVersion === null)
      throw new ToolInputError('"baseVersion" is required: read the document first')
    const title = optionalStr(args, 'title')
    const artifact = ctx.app.reviseArtifact(artifactId, {
      content: str(args, 'content'),
      reason: str(args, 'reason'),
      author: ctx.conversationId,
      ...(title ? { title } : {}),
      baseVersion,
    })
    return json({ artifactId, version: artifact.version })
  },
}

const readDocument: Tool = {
  spec: {
    name: 'read_document',
    description: 'Read a document of the project, at its newest version or an older one.',
    parameters: {
      type: 'object',
      properties: { artifactId: { type: 'string' }, version: { type: 'integer' } },
      required: ['artifactId'],
    },
  },
  run(args, ctx) {
    const version = optionalInt(args, 'version')
    const artifact = ctx.app.getArtifact(str(args, 'artifactId'), version ?? undefined)
    if (artifact.projectId !== ctx.projectId) {
      return { content: `No document ${artifact.id} in this project.`, isError: true }
    }
    return json({
      artifactId: artifact.id,
      title: artifact.title,
      version: artifact.version,
      updatedBy: artifact.updatedBy,
      content: artifact.content,
    })
  },
}

const listArtifacts: Tool = {
  spec: {
    name: 'list_artifacts',
    description: "List the project's documents, newest first.",
    parameters: { type: 'object', properties: {} },
  },
  run(_args, ctx) {
    return json(
      ctx.app.listArtifacts(ctx.projectId).map((a) => ({
        artifactId: a.id,
        kind: a.kind,
        title: a.title,
        version: a.version,
        nodeId: a.nodeId,
        updatedBy: a.updatedBy,
      })),
    )
  },
}

export const artifactTools: Tool[] = [createDocument, updateDocument, readDocument, listArtifacts]
