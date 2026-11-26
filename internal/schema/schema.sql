CREATE TABLE artifact_comments (
      id TEXT PRIMARY KEY,
      artifact_id TEXT NOT NULL,
      quote TEXT NOT NULL,
      -- On a whiteboard: the item or connector (JSON), see ArtifactCommentAnchor.
      anchor TEXT,
      comments TEXT NOT NULL,
      resolved_by TEXT,
      resolved_at INTEGER,
      reopened_by TEXT,
      reopened_at INTEGER,
      created_at INTEGER NOT NULL
    );

CREATE TABLE artifact_feedback (
      artifact_id TEXT NOT NULL,
      round INTEGER NOT NULL,
      version INTEGER NOT NULL,
      notify TEXT NOT NULL,
      items TEXT NOT NULL,
      images TEXT NOT NULL,
      fingerprints TEXT NOT NULL DEFAULT '[]',
      withdrawn_at INTEGER,
      event_seq INTEGER NOT NULL DEFAULT 0,
      done_by TEXT,
      done_at INTEGER,
      summary TEXT,
      created_at INTEGER NOT NULL,
      PRIMARY KEY (artifact_id, round)
    );

CREATE TABLE artifact_shared (
    artifact_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    version INTEGER NOT NULL,
    state BLOB NOT NULL,
    updated_at INTEGER NOT NULL
  );

CREATE TABLE artifact_versions (
      artifact_id TEXT NOT NULL,
      version INTEGER NOT NULL,
      title TEXT NOT NULL,
      content TEXT NOT NULL,
      author TEXT NOT NULL,
      reason TEXT NOT NULL,
      created_at INTEGER NOT NULL,
      PRIMARY KEY (artifact_id, version)
    );

CREATE TABLE artifacts (
      id TEXT PRIMARY KEY,
      project_id TEXT NOT NULL,
      kind TEXT NOT NULL,
      title TEXT NOT NULL,
      node_id TEXT,
      version INTEGER NOT NULL,
      content TEXT NOT NULL,
      -- The summary lists show (JSON), worked out once per version rather than per list.
      preview TEXT NOT NULL,
      created_by TEXT NOT NULL,
      updated_by TEXT NOT NULL,
      -- The agent that wrote it last; it hears about the user's edits.
      last_agent TEXT,
      open_comments INTEGER NOT NULL DEFAULT 0,
      commented_at INTEGER NOT NULL DEFAULT 0,
      has_prototype INTEGER NOT NULL DEFAULT 0,
      pending_actions INTEGER NOT NULL DEFAULT 0,
      created_at INTEGER NOT NULL,
      updated_at INTEGER NOT NULL
    );

CREATE TABLE attention_waiters (
      attention_id TEXT NOT NULL,
      thread_id TEXT NOT NULL,
      PRIMARY KEY (attention_id, thread_id)
    );

CREATE TABLE attentions (
      id TEXT PRIMARY KEY,
      project_id TEXT NOT NULL,
      thread_id TEXT,
      conversation_id TEXT,
      message_id TEXT,
      block TEXT NOT NULL,
      closed INTEGER NOT NULL DEFAULT 0,
      answer TEXT,
      withdrawn TEXT,
      approved INTEGER,
      purpose TEXT,
      created_at INTEGER NOT NULL,
      -- The event that last changed what the row shows, for pages that fetch only changes.
      changed_seq INTEGER NOT NULL DEFAULT 0
    );

CREATE TABLE conversations (
      id TEXT PRIMARY KEY,
      project_id TEXT NOT NULL,
      role TEXT NOT NULL,
      pending_seq INTEGER NOT NULL DEFAULT 0,
      pending_since INTEGER NOT NULL DEFAULT 0,
      turn_id TEXT,
      turn_started_seq INTEGER,
      turn_started_at INTEGER,
      -- When its newest turn started, kept after it ends: was there a report since?
      last_turn_at INTEGER,
      model TEXT,
      last_outcome TEXT,
      last_error TEXT,
      input_tokens INTEGER NOT NULL DEFAULT 0,
      output_tokens INTEGER NOT NULL DEFAULT 0,
      cached_tokens INTEGER NOT NULL DEFAULT 0,
      cache_write_tokens INTEGER NOT NULL DEFAULT 0,
      reasoning_tokens INTEGER NOT NULL DEFAULT 0,
      cost_usd REAL NOT NULL DEFAULT 0,
      cost_cny REAL NOT NULL DEFAULT 0,
      context_tokens INTEGER NOT NULL DEFAULT 0,
      instructions TEXT,
      activity TEXT,
      -- The turn went on with the fallback model.
      fallback INTEGER NOT NULL DEFAULT 0,
      compactions INTEGER NOT NULL DEFAULT 0,
      wait_id TEXT,
      wait_until INTEGER,
      wait_note TEXT
    );

CREATE TABLE events (
      seq INTEGER PRIMARY KEY AUTOINCREMENT,
      id TEXT NOT NULL UNIQUE,
      type TEXT NOT NULL,
      project_id TEXT NOT NULL,
      data TEXT NOT NULL,
      at INTEGER NOT NULL
    );

CREATE TABLE files (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    mime TEXT NOT NULL,
    size INTEGER NOT NULL,
    sha256 TEXT NOT NULL,
    data BLOB NOT NULL,
    created_at INTEGER NOT NULL
  );

CREATE TABLE issue_writes (
      attention_id TEXT PRIMARY KEY,
      project_id TEXT NOT NULL,
      purpose TEXT NOT NULL,
      thread_id TEXT NOT NULL,
      tracker TEXT NOT NULL,
      number INTEGER NOT NULL,
      action TEXT NOT NULL,
      status TEXT NOT NULL,
      url TEXT,
      written_seq INTEGER
    );

CREATE TABLE memories (
      id TEXT PRIMARY KEY,
      project_id TEXT NOT NULL,
      text TEXT NOT NULL,
      author TEXT NOT NULL,
      created_at INTEGER NOT NULL,
      updated_at INTEGER NOT NULL
    );

CREATE TABLE messages (
      id TEXT PRIMARY KEY,
      project_id TEXT NOT NULL,
      conversation_id TEXT NOT NULL,
      author TEXT NOT NULL,
      kind TEXT NOT NULL,
      content TEXT NOT NULL,
      created_at INTEGER NOT NULL,
      edited_at INTEGER,
      edit_reason TEXT,
      original_content TEXT,
      read_at INTEGER,
      edited_since_read INTEGER NOT NULL DEFAULT 0,
      blocks TEXT,
      attachments TEXT,
      mentions TEXT,
      comments TEXT,
      -- On a relay: the user's messages it passes along. On a user's message: the relays
      -- that passed it to threads.
      cites TEXT,
      relayed_to TEXT,
      -- The message it answers (JSON QuotedMessage), and the emojis on it (JSON Reaction[]).
      reply_to TEXT,
      reactions TEXT,
      -- The event that last changed the row, for pages that fetch only changes.
      changed_seq INTEGER NOT NULL DEFAULT 0
    );

CREATE TABLE notices (
      id TEXT PRIMARY KEY,
      project_id TEXT NOT NULL,
      conversation_id TEXT NOT NULL,
      kind TEXT NOT NULL,
      payload TEXT NOT NULL,
      undone INTEGER NOT NULL DEFAULT 0,
      created_at INTEGER NOT NULL,
      -- The event that last changed the row, for pages that fetch only changes.
      changed_seq INTEGER NOT NULL DEFAULT 0
    );

CREATE TABLE plan_edges (
      project_id TEXT NOT NULL,
      from_id TEXT NOT NULL,
      to_id TEXT NOT NULL,
      changed_in TEXT,
      PRIMARY KEY (project_id, from_id, to_id)
    );

CREATE TABLE plan_node_issues (
      project_id TEXT NOT NULL,
      node_id TEXT NOT NULL,
      tracker TEXT NOT NULL,
      number INTEGER NOT NULL,
      title TEXT NOT NULL,
      url TEXT NOT NULL,
      linked_by TEXT NOT NULL,
      seq INTEGER NOT NULL,
      PRIMARY KEY (project_id, node_id, tracker, number)
    );

CREATE TABLE plan_nodes (
      id TEXT NOT NULL,
      project_id TEXT NOT NULL,
      kind TEXT NOT NULL,
      title TEXT NOT NULL,
      description TEXT NOT NULL,
      acceptance TEXT NOT NULL,
      group_name TEXT NOT NULL DEFAULT '',
      position INTEGER NOT NULL,
      changed_in TEXT,
      created_at INTEGER NOT NULL,
      updated_at INTEGER NOT NULL,
      PRIMARY KEY (project_id, id)
    );

CREATE TABLE plan_proposals (
      id TEXT PRIMARY KEY,
      project_id TEXT NOT NULL,
      attention_id TEXT NOT NULL UNIQUE,
      reason TEXT NOT NULL,
      ops TEXT NOT NULL,
      revert_of TEXT,
      status TEXT NOT NULL,
      stale_reason TEXT,
      created_at INTEGER NOT NULL
    );

CREATE TABLE plan_revisions (
      id TEXT PRIMARY KEY,
      project_id TEXT NOT NULL,
      author TEXT NOT NULL,
      reason TEXT NOT NULL,
      ops TEXT NOT NULL,
      inverse TEXT NOT NULL,
      revert_of TEXT,
      reverted_by TEXT,
      proposal_id TEXT,
      seq INTEGER NOT NULL,
      created_at INTEGER NOT NULL
    );

CREATE TABLE projects (
      id TEXT PRIMARY KEY,
      name TEXT NOT NULL,
      coordinator_id TEXT NOT NULL,
      source TEXT,
      -- Sources the project only reads, as JSON.
      reference_sources TEXT NOT NULL DEFAULT '[]',
      slots INTEGER,
      mcp TEXT NOT NULL DEFAULT '[]',
      settings TEXT,
      -- What Ariadne found out about the repository itself (JSON, ProjectDetected).
      detected TEXT,
      -- The user paused its autopilot; and the event at which it last went on (switched on
      -- or resumed).
      autopilot_paused INTEGER NOT NULL DEFAULT 0,
      autopilot_since INTEGER,
      -- Its name is only the start of the user's first words, until someone names it.
      unnamed INTEGER NOT NULL DEFAULT 0,
      created_at INTEGER NOT NULL,
      updated_at INTEGER NOT NULL
    );

CREATE TABLE prompts (
      project_id TEXT NOT NULL,
      hash TEXT NOT NULL,
      content TEXT NOT NULL,
      PRIMARY KEY (project_id, hash)
    );

CREATE TABLE push_subscriptions (
      id TEXT PRIMARY KEY,
      endpoint TEXT NOT NULL UNIQUE,
      p256dh TEXT NOT NULL,
      auth TEXT NOT NULL,
      label TEXT NOT NULL DEFAULT '',
      user_agent TEXT NOT NULL DEFAULT '',
      created_at INTEGER NOT NULL,
      last_used_at INTEGER,
      last_error TEXT
    );

CREATE TABLE reviews (
      id INTEGER PRIMARY KEY,
      project_id TEXT NOT NULL,
      thread_id TEXT NOT NULL,
      reviewer_id TEXT NOT NULL,
      round INTEGER NOT NULL,
      head TEXT,
      requested_at INTEGER NOT NULL,
      verdict TEXT,
      summary TEXT,
      comments TEXT NOT NULL DEFAULT '[]',
      replies TEXT,
      submitted_at INTEGER,
      abandoned_at INTEGER
    );

CREATE TABLE search_docs (
      id INTEGER PRIMARY KEY,
      project_id TEXT NOT NULL,
      kind TEXT NOT NULL,
      ref_id TEXT NOT NULL UNIQUE,
      conversation_id TEXT,
      at INTEGER NOT NULL
    );

CREATE VIRTUAL TABLE search_text USING fts5(title, body, tokenize = 'trigram');

CREATE TABLE thread_repo_baselines (
      project_id TEXT NOT NULL,
      thread_id TEXT NOT NULL,
      source_id TEXT NOT NULL,
      repo_path TEXT NOT NULL,
      base_head TEXT NOT NULL,
      base_branch TEXT NOT NULL DEFAULT '',
      PRIMARY KEY (thread_id, source_id, repo_path)
    );

CREATE TABLE threads (
      id TEXT PRIMARY KEY,
      project_id TEXT NOT NULL,
      title TEXT NOT NULL,
      node_id TEXT,
      started INTEGER NOT NULL DEFAULT 0,
      last_report TEXT,
      last_summary TEXT,
      last_headline TEXT,
      -- Ids of the artifacts it pointed the user to (JSON), shown by their current titles.
      last_artifacts TEXT NOT NULL DEFAULT '[]',
      reported_at INTEGER,
      reported_head TEXT,
      -- When it was last told to report after a turn that ended without one.
      nudged_at INTEGER,
      workspace_source_id TEXT,
      workspace_label TEXT,
      workspace_branch TEXT,
      workspace_base TEXT,
      workspace_removed INTEGER NOT NULL DEFAULT 0,
      workspace_parked INTEGER NOT NULL DEFAULT 0,
      merged TEXT,
      -- When its work last landed (its branch or pull request was merged), and when the
      -- user last wrote to it: landed with nothing written since, it is archived.
      landed_at INTEGER,
      user_at INTEGER,
      -- What its workspace changes against the base (JSON ChangeTotals), as of its last
      -- turn that changed something.
      changes TEXT,
      pull_request TEXT,
      -- The sources it alone reads (JSON ThreadReference[]).
      reference_sources TEXT NOT NULL DEFAULT '[]',
      reference_ended TEXT NOT NULL DEFAULT '[]',
      check_failures INTEGER NOT NULL DEFAULT 0,
      stopped_reason TEXT,
      stopped_by TEXT,
      stopped_at INTEGER,
      reviewed_at INTEGER,
      model TEXT,
      effort TEXT,
      branch_slug TEXT,
      -- For a reviewing thread: the author it reviews.
      review_of TEXT,
      -- For an author: its reviewing thread, the rounds of its latest delivery, whether a
      -- round runs now, and whether that delivery reached the user.
      reviewer_id TEXT,
      review_rounds INTEGER NOT NULL DEFAULT 0,
      reviewing INTEGER NOT NULL DEFAULT 0,
      review_delivered INTEGER NOT NULL DEFAULT 1,
      -- The branch commit the autopilot last left for the user (empty for none), so it does
      -- not try that commit again.
      autopilot_held TEXT,
      auto INTEGER NOT NULL DEFAULT 0,
      -- Set aside until this time, why and by whom; due once that time came.
      aside_until INTEGER,
      aside_reason TEXT,
      aside_by TEXT,
      aside_due INTEGER NOT NULL DEFAULT 0,
      created_at INTEGER NOT NULL,
      updated_at INTEGER NOT NULL,
      -- The event that last changed what the thread view shows, for incremental sync.
      changed_seq INTEGER NOT NULL DEFAULT 0
    );

CREATE TABLE todos (
      conversation_id TEXT PRIMARY KEY,
      project_id TEXT NOT NULL,
      items TEXT NOT NULL,
      updated_at INTEGER NOT NULL
    );

CREATE TABLE transcript (
      id INTEGER PRIMARY KEY AUTOINCREMENT,
      project_id TEXT NOT NULL,
      conversation_id TEXT NOT NULL,
      seq INTEGER NOT NULL,
      kind TEXT NOT NULL,
      ref TEXT,
      payload TEXT,
      woke INTEGER NOT NULL DEFAULT 0
    );

CREATE TABLE used_approvals (
      attention_id TEXT PRIMARY KEY,
      thread_id TEXT NOT NULL
    );

CREATE INDEX artifact_comments_artifact ON artifact_comments (artifact_id, created_at);

CREATE INDEX artifact_feedback_pending ON artifact_feedback (notify, created_at, artifact_id, round)
  WHERE done_at IS NULL;

CREATE INDEX artifacts_project ON artifacts (project_id, updated_at);

CREATE INDEX artifacts_updated ON artifacts (updated_at DESC, id DESC);

CREATE INDEX artifacts_created ON artifacts (created_at DESC, id DESC);

CREATE INDEX attentions_message ON attentions (message_id);

CREATE INDEX attentions_project ON attentions (project_id, closed);

CREATE INDEX attentions_conversation_approval ON attentions (conversation_id, created_at)
  WHERE message_id IS NULL AND purpose IS NOT NULL;

CREATE INDEX attention_waiters_thread ON attention_waiters (thread_id, attention_id);

CREATE INDEX conversations_project ON conversations (project_id);

CREATE INDEX events_project ON events (project_id, seq);

CREATE INDEX files_hash ON files (project_id, sha256);

CREATE INDEX memories_project ON memories (project_id, created_at);

CREATE INDEX messages_conversation ON messages (conversation_id, id);

CREATE INDEX notices_conversation ON notices (conversation_id, created_at);

CREATE INDEX plan_nodes_project ON plan_nodes (project_id, position);

CREATE INDEX plan_revisions_project ON plan_revisions (project_id, seq);

CREATE INDEX reviews_thread ON reviews (thread_id, id);

CREATE INDEX search_docs_project ON search_docs (project_id, kind, at);

CREATE INDEX search_docs_kind_at ON search_docs (kind, at DESC);

CREATE INDEX threads_project ON threads (project_id, id);

CREATE INDEX threads_project_changed ON threads (project_id, changed_seq);

CREATE INDEX transcript_conversation ON transcript (conversation_id, id);
