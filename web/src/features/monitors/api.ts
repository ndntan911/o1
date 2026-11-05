import { apiRequest } from '@/lib/api/client';
import type {
	CreateMonitorInput,
	Monitor,
	MonitorListResponse,
	ResultsResponse,
	ResultsWindow,
	Summary,
	UpdateMonitorInput,
} from '@/lib/api/types';

function monitorPath(workspaceId: string, monitorId: string): string {
	return `/workspaces/${workspaceId}/monitors/${monitorId}`;
}

export function listMonitors(
	workspaceId: string,
	cursor?: string,
	signal?: AbortSignal,
): Promise<MonitorListResponse> {
	return apiRequest<MonitorListResponse>(
		`/workspaces/${workspaceId}/monitors`,
		{
			query: { cursor, limit: 50 },
			signal,
		},
	);
}

export function getMonitor(
	workspaceId: string,
	monitorId: string,
	signal?: AbortSignal,
): Promise<Monitor> {
	return apiRequest<Monitor>(monitorPath(workspaceId, monitorId), { signal });
}

export function createMonitor(
	workspaceId: string,
	input: CreateMonitorInput,
): Promise<Monitor> {
	return apiRequest<Monitor>(`/workspaces/${workspaceId}/monitors`, {
		method: 'POST',
		body: input,
	});
}

export function updateMonitor(
	workspaceId: string,
	monitorId: string,
	input: UpdateMonitorInput,
): Promise<Monitor> {
	return apiRequest<Monitor>(monitorPath(workspaceId, monitorId), {
		method: 'PATCH',
		body: input,
	});
}

export function deleteMonitor(
	workspaceId: string,
	monitorId: string,
): Promise<void> {
	return apiRequest<void>(monitorPath(workspaceId, monitorId), {
		method: 'DELETE',
	});
}

export function pauseMonitor(
	workspaceId: string,
	monitorId: string,
): Promise<Monitor> {
	return apiRequest<Monitor>(`${monitorPath(workspaceId, monitorId)}/pause`, {
		method: 'POST',
	});
}

export function resumeMonitor(
	workspaceId: string,
	monitorId: string,
): Promise<Monitor> {
	return apiRequest<Monitor>(`${monitorPath(workspaceId, monitorId)}/resume`, {
		method: 'POST',
	});
}

export function getResults(
	workspaceId: string,
	monitorId: string,
	resultsWindow: ResultsWindow,
	cursor?: string,
	signal?: AbortSignal,
): Promise<ResultsResponse> {
	return apiRequest<ResultsResponse>(
		`${monitorPath(workspaceId, monitorId)}/results`,
		{
			query: { window: resultsWindow, cursor, limit: 100 },
			signal,
		},
	);
}

export function getSummary(
	workspaceId: string,
	monitorId: string,
	resultsWindow: ResultsWindow,
	signal?: AbortSignal,
): Promise<Summary> {
	return apiRequest<Summary>(`${monitorPath(workspaceId, monitorId)}/summary`, {
		query: { window: resultsWindow },
		signal,
	});
}
