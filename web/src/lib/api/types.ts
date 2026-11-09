export type Role = 'viewer' | 'member' | 'admin' | 'owner';
export type HttpMethod = 'GET' | 'HEAD' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';
export type MonitorStatus = 'up' | 'down' | 'unknown' | 'paused';
export type ResultsWindow = '1h' | '6h' | '24h' | '7d';

export interface User {
	id: string;
	email: string;
	is_admin: boolean;
}

export interface Workspace {
	id: string;
	name: string;
	slug: string;
	role: Role;
}

export interface Me {
	user: User;
	workspaces: Workspace[];
}

export interface Member {
	user_id: string;
	email: string;
	role: Role;
}

export interface LastCheck {
	checked_at: string;
	status_code: number | null;
	latency_ms: number | null;
	error: string | null;
}

export interface Summary {
	window: string;
	uptime: number | null;
	checks: number;
	up: number;
	down: number;
	avg_latency_ms: number;
	p95_latency_ms: number;
	min_latency_ms: number;
	max_latency_ms: number;
}

export interface Monitor {
	id: string;
	workspace_id: string;
	name: string;
	url: string;
	method: HttpMethod;
	headers: Record<string, string>;
	has_body: boolean;
	content_type: string | null;
	expected_status: string;
	interval_seconds: number;
	timeout_seconds: number;
	confirmation_threshold: number;
	recovery_threshold: number;
	paused: boolean;
	status: MonitorStatus;
	last_check: LastCheck | null;
	summary_24h: Summary | null;
	created_at: string;
	updated_at: string;
}

export interface ProbeResult {
	id: string;
	checked_at: string;
	scheduled_for: string;
	status: 'up' | 'down';
	status_code: number | null;
	latency_ms: number;
	error: string | null;
}

export interface CreateMonitorInput {
	name: string;
	url: string;
	method: HttpMethod;
	headers?: Record<string, string>;
	body?: string | null;
	content_type?: string | null;
	expected_status: string;
	interval_seconds: number;
	timeout_seconds: number;
	confirmation_threshold: number;
	recovery_threshold: number;
}

export interface UpdateMonitorInput {
	name?: string;
	url?: string;
	method?: HttpMethod;
	headers?: Record<string, string>;
	body?: string;
	content_type?: string;
	expected_status?: string;
	interval_seconds?: number;
	timeout_seconds?: number;
	confirmation_threshold?: number;
	recovery_threshold?: number;
}

export interface MonitorListResponse {
	monitors: Monitor[];
	next_cursor?: string;
}

export interface ResultsResponse {
	results: ProbeResult[];
	next_cursor?: string;
}

export interface WorkspaceListResponse {
	workspaces: Workspace[];
}

export interface MemberListResponse {
	members: Member[];
}

export interface AdminWorkspace {
	id: string;
	name: string;
	slug: string;
	created_by: string;
	created_at: string;
	monitor_limit: number;
	suspended_at: string | null;
	suspended_by: string | null;
	suspended_reason: string | null;
	member_count: number;
	monitor_count: number;
}

export interface AdminMonitor {
	id: string;
	workspace_id: string;
	workspace_name: string;
	workspace_slug: string;
	name: string;
	url: string;
	method: HttpMethod;
	expected_status: string;
	interval_seconds: number;
	timeout_seconds: number;
	paused: boolean;
	status: MonitorStatus;
	last_checked_at: string | null;
	last_status_code: number | null;
	last_latency_ms: number | null;
	last_error: string | null;
	created_at: string;
	updated_at: string;
}

export interface SecurityEvent {
	id: string;
	workspace_id: string | null;
	actor_user_id: string | null;
	kind: string;
	detail: unknown;
	created_at: string;
}

export interface AdminWorkspaceListResponse {
	workspaces: AdminWorkspace[];
	next_cursor?: string;
}

export interface AdminMonitorListResponse {
	monitors: AdminMonitor[];
	next_cursor?: string;
}

export interface SecurityEventListResponse {
	events: SecurityEvent[];
	next_cursor?: string;
}

export interface AdminWorkspaceFilters {
	query: string;
	suspended?: boolean;
}

export interface AdminMonitorFilters {
	query: string;
	workspaceId?: string;
}

export interface SecurityEventFilters {
	kind?: string;
	workspaceId?: string;
}

export interface Incident {
	id: string;
	workspace_id: string;
	monitor_id: string;
	monitor_name: string;
	status: 'open' | 'acknowledged' | 'resolved';
	started_at: string;
	acknowledged_at: string | null;
	acknowledged_by: string | null;
	resolved_at: string | null;
	last_notified_at: string | null;
	reminder_count: number;
}

export interface IncidentEvent {
	id: string;
	kind: 'opened' | 'acknowledged' | 'resolved';
	actor_user_id: string | null;
	detail: unknown;
	created_at: string;
}

export interface IncidentDetail {
	incident: Incident;
	events: IncidentEvent[];
}

export interface IncidentDelivery {
	id: string;
	event: 'opened' | 'reminder' | 'resolved';
	sequence: number;
	channel_id: string;
	channel_kind: AlertChannelKind;
	channel_name: string;
	recipient: string;
	status: 'pending' | 'sent' | 'failed' | 'skipped';
	attempts: number;
	last_error: string | null;
	sent_at: string | null;
	created_at: string;
}

export interface IncidentFilters {
	status: 'active' | 'open' | 'acknowledged' | 'resolved' | 'all';
	monitorId?: string;
}

export interface IncidentListResponse {
	incidents: Incident[];
	next_cursor?: string;
}

export interface IncidentDeliveryListResponse {
	deliveries: IncidentDelivery[];
}

export type AlertChannelKind = 'email' | 'webhook' | 'slack' | 'telegram';

export interface AlertChannel {
	id: string;
	kind: AlertChannelKind;
	name: string;
	enabled: boolean;
	recipients?: string[];
	url_preview?: string;
	signing_secret_set?: boolean;
	bot_token_set?: boolean;
	chat_id?: string;
	created_at: string;
	updated_at: string;
}

export interface MonitorAlertState {
	available: boolean;
	channel_enabled: boolean;
	enabled: boolean;
}

export interface MonitorChannel {
	id: string;
	kind: AlertChannelKind;
	name: string;
	channel_enabled: boolean;
	enabled: boolean;
}

export interface MonitorChannelListResponse {
	channels: MonitorChannel[];
}

export interface StatusPageConfig {
	enabled: boolean;
	title: string;
	description: string;
	monitor_ids: string[];
}

export interface StatusPageConfigInput {
	enabled: boolean;
	title: string;
	description: string;
	monitor_ids: string[];
}

export type PublicStatusDayStatus = 'up' | 'partial' | 'down' | 'no_data';
export type PublicMonitorStatus = 'up' | 'down' | 'unknown' | 'paused';

export interface PublicStatusDay {
	date: string;
	status: PublicStatusDayStatus;
	uptime: number | null;
}

export interface PublicStatusMonitor {
	id: string;
	name: string;
	status: PublicMonitorStatus;
	uptime: number | null;
	days: PublicStatusDay[];
}

export interface PublicStatusIncident {
	id: string;
	monitor_id: string;
	monitor_name: string;
	status: 'ongoing' | 'resolved';
	started_at: string;
	resolved_at: string | null;
}

export interface PublicStatusPage {
	workspace: { name: string; slug: string };
	page: { title: string; description: string; updated_at: string };
	monitors: PublicStatusMonitor[];
	incidents: { active: PublicStatusIncident[]; recent: PublicStatusIncident[] };
}

export type APIKeyRole = 'viewer' | 'member';

export interface APIKey {
	id: string;
	name: string;
	prefix: string;
	role: APIKeyRole;
	created_by: string;
	created_at: string;
	last_used_at: string | null;
}

export interface CreatedAPIKey {
	key: APIKey;
	secret: string;
}

export interface CreateAPIKeyInput {
	name: string;
	role: APIKeyRole;
}
