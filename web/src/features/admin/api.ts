import { apiRequest } from '@/lib/api/client';
import type {
	AdminMonitorFilters,
	AdminMonitorListResponse,
	AdminWorkspaceFilters,
	AdminWorkspaceListResponse,
	SecurityEventFilters,
	SecurityEventListResponse,
} from '@/lib/api/types';

export const SECURITY_EVENT_KINDS = [
	'monitor_created',
	'monitor_create_rate_limited',
	'ssrf_blocked',
	'workspace_suspended',
	'workspace_unsuspended',
	'login_succeeded',
	'login_failed',
	'member_added',
	'member_role_changed',
	'monitor_deleted',
	'alert_channel_created',
	'alert_channel_updated',
	'alert_channel_deleted',
	'status_page_updated',
	'api_key_created',
	'api_key_revoked',
	'totp_enabled',
	'totp_disabled',
	'totp_recovery_codes_regenerated',
] as const;

export type SecurityEventKind = (typeof SECURITY_EVENT_KINDS)[number];

type QueryValue = string | number | boolean | null | undefined;

function appendQuery(
	query: Record<string, QueryValue>,
	key: string,
	value: QueryValue,
): void {
	if (value === undefined || value === null) return;
	if (typeof value === 'string' && value.trim().length === 0) return;
	query[key] = value;
}

function workspaceQuery(
	filters: AdminWorkspaceFilters,
	cursor?: string,
): Record<string, QueryValue> {
	const query: Record<string, QueryValue> = { limit: 50 };
	appendQuery(query, 'q', filters.query);
	appendQuery(query, 'suspended', filters.suspended);
	appendQuery(query, 'cursor', cursor);
	return query;
}

function monitorQuery(
	filters: AdminMonitorFilters,
	cursor?: string,
): Record<string, QueryValue> {
	const query: Record<string, QueryValue> = { limit: 50 };
	appendQuery(query, 'q', filters.query);
	appendQuery(query, 'workspace_id', filters.workspaceId);
	appendQuery(query, 'cursor', cursor);
	return query;
}

function securityEventQuery(
	filters: SecurityEventFilters,
	cursor?: string,
): Record<string, QueryValue> {
	const query: Record<string, QueryValue> = { limit: 50 };
	appendQuery(query, 'kind', filters.kind);
	appendQuery(query, 'workspace_id', filters.workspaceId);
	appendQuery(query, 'cursor', cursor);
	return query;
}

export function listAdminWorkspaces(
	filters: AdminWorkspaceFilters,
	cursor?: string,
	signal?: AbortSignal,
): Promise<AdminWorkspaceListResponse> {
	return apiRequest<AdminWorkspaceListResponse>('/admin/workspaces', {
		query: workspaceQuery(filters, cursor),
		signal,
	});
}

export function suspendWorkspace(
	workspaceId: string,
	reason: string,
): Promise<void> {
	return apiRequest<void>(`/admin/workspaces/${workspaceId}/suspend`, {
		method: 'POST',
		body: { reason },
	});
}

export function unsuspendWorkspace(workspaceId: string): Promise<void> {
	return apiRequest<void>(`/admin/workspaces/${workspaceId}/unsuspend`, {
		method: 'POST',
	});
}

export function listAdminMonitors(
	filters: AdminMonitorFilters,
	cursor?: string,
	signal?: AbortSignal,
): Promise<AdminMonitorListResponse> {
	return apiRequest<AdminMonitorListResponse>('/admin/monitors', {
		query: monitorQuery(filters, cursor),
		signal,
	});
}

export function listSecurityEvents(
	filters: SecurityEventFilters,
	cursor?: string,
	signal?: AbortSignal,
): Promise<SecurityEventListResponse> {
	return apiRequest<SecurityEventListResponse>('/admin/security-events', {
		query: securityEventQuery(filters, cursor),
		signal,
	});
}
