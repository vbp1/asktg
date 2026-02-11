export namespace domain {
	
	export class ChatFolder {
	    id: number;
	    title: string;
	    emoticon?: string;
	    color?: number;
	    chat_ids: number[];
	    pinned_chat_ids?: number[];
	
	    static createFrom(source: any = {}) {
	        return new ChatFolder(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.title = source["title"];
	        this.emoticon = source["emoticon"];
	        this.color = source["color"];
	        this.chat_ids = source["chat_ids"];
	        this.pinned_chat_ids = source["pinned_chat_ids"];
	    }
	}
	export class ChatPolicy {
	    chat_id: number;
	    title: string;
	    type: string;
	    enabled: boolean;
	    history_mode: string;
	    allow_embeddings: boolean;
	    urls_mode: string;
	    sync_cursor: string;
	    last_message_unix: number;
	    last_synced_unix: number;
	
	    static createFrom(source: any = {}) {
	        return new ChatPolicy(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.chat_id = source["chat_id"];
	        this.title = source["title"];
	        this.type = source["type"];
	        this.enabled = source["enabled"];
	        this.history_mode = source["history_mode"];
	        this.allow_embeddings = source["allow_embeddings"];
	        this.urls_mode = source["urls_mode"];
	        this.sync_cursor = source["sync_cursor"];
	        this.last_message_unix = source["last_message_unix"];
	        this.last_synced_unix = source["last_synced_unix"];
	    }
	}
	export class EmbeddingsConfig {
	    base_url: string;
	    model: string;
	    dimensions: number;
	    configured: boolean;
	
	    static createFrom(source: any = {}) {
	        return new EmbeddingsConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.base_url = source["base_url"];
	        this.model = source["model"];
	        this.dimensions = source["dimensions"];
	        this.configured = source["configured"];
	    }
	}
	export class EmbeddingsTestResult {
	    ok: boolean;
	    base_url: string;
	    model: string;
	    dimensions: number;
	    vector_len: number;
	    took_ms: number;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new EmbeddingsTestResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.base_url = source["base_url"];
	        this.model = source["model"];
	        this.dimensions = source["dimensions"];
	        this.vector_len = source["vector_len"];
	        this.took_ms = source["took_ms"];
	        this.error = source["error"];
	    }
	}
	export class IndexStatus {
	    sync_state: string;
	    backfill_progress: number;
	    queue_depth: number;
	    last_sync_unix: number;
	    mcp_endpoint: string;
	    mcp_enabled: boolean;
	    mcp_status: string;
	    mcp_port: number;
	    message_count: number;
	    indexed_chunk_count: number;
	    updated_at_unix: number;
	
	    static createFrom(source: any = {}) {
	        return new IndexStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sync_state = source["sync_state"];
	        this.backfill_progress = source["backfill_progress"];
	        this.queue_depth = source["queue_depth"];
	        this.last_sync_unix = source["last_sync_unix"];
	        this.mcp_endpoint = source["mcp_endpoint"];
	        this.mcp_enabled = source["mcp_enabled"];
	        this.mcp_status = source["mcp_status"];
	        this.mcp_port = source["mcp_port"];
	        this.message_count = source["message_count"];
	        this.indexed_chunk_count = source["indexed_chunk_count"];
	        this.updated_at_unix = source["updated_at_unix"];
	    }
	}
	export class Message {
	    chat_id: number;
	    msg_id: number;
	    timestamp: number;
	    edit_ts: number;
	    sender_id: number;
	    sender_display: string;
	    text: string;
	    deleted: boolean;
	    has_url: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Message(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.chat_id = source["chat_id"];
	        this.msg_id = source["msg_id"];
	        this.timestamp = source["timestamp"];
	        this.edit_ts = source["edit_ts"];
	        this.sender_id = source["sender_id"];
	        this.sender_display = source["sender_display"];
	        this.text = source["text"];
	        this.deleted = source["deleted"];
	        this.has_url = source["has_url"];
	    }
	}
	export class OnboardingStatus {
	    completed: boolean;
	    telegram_configured: boolean;
	    telegram_authorized: boolean;
	    chats_discovered: number;
	    enabled_chats: number;
	
	    static createFrom(source: any = {}) {
	        return new OnboardingStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.completed = source["completed"];
	        this.telegram_configured = source["telegram_configured"];
	        this.telegram_authorized = source["telegram_authorized"];
	        this.chats_discovered = source["chats_discovered"];
	        this.enabled_chats = source["enabled_chats"];
	    }
	}
	export class SearchResult {
	    chat_id: number;
	    msg_id: number;
	    timestamp: number;
	    chat_title: string;
	    sender: string;
	    snippet: string;
	    message_text: string;
	    source_type?: string;
	    url?: string;
	    url_final?: string;
	    url_title?: string;
	    score: number;
	    deep_link?: string;
	    match_fts?: boolean;
	    match_semantic?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SearchResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.chat_id = source["chat_id"];
	        this.msg_id = source["msg_id"];
	        this.timestamp = source["timestamp"];
	        this.chat_title = source["chat_title"];
	        this.sender = source["sender"];
	        this.snippet = source["snippet"];
	        this.message_text = source["message_text"];
	        this.source_type = source["source_type"];
	        this.url = source["url"];
	        this.url_final = source["url_final"];
	        this.url_title = source["url_title"];
	        this.score = source["score"];
	        this.deep_link = source["deep_link"];
	        this.match_fts = source["match_fts"];
	        this.match_semantic = source["match_semantic"];
	    }
	}
	export class TelegramAuthStatus {
	    configured: boolean;
	    authorized: boolean;
	    awaiting_code: boolean;
	    phone: string;
	    user_display: string;
	
	    static createFrom(source: any = {}) {
	        return new TelegramAuthStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.configured = source["configured"];
	        this.authorized = source["authorized"];
	        this.awaiting_code = source["awaiting_code"];
	        this.phone = source["phone"];
	        this.user_display = source["user_display"];
	    }
	}

}

