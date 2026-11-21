import { apiRequest } from '@/lib/api/client';
import type {
	IncidentInsight,
	IncidentDelivery,
	IncidentDeliveryListResponse,
	IncidentDetail,
	IncidentFilters,
	IncidentListResponse,
} from '@/lib/api/types';

function incidentsPath(workspaceId: string): string {
	return `/workspaces/${workspaceId}/incidents`;
}

function incidentPath(workspaceId: string, incidentId: string): string {
	return `${incidentsPath(workspaceId)}/${incidentId}`;
}

export function listIncidents(
	workspaceId: string,
	filters: IncidentFilters,
	cursor?: string,
	signal?: AbortSignal,
	limit = 50,
): Promise<IncidentListResponse> {
	return apiRequest<IncidentListResponse>(incidentsPath(workspaceId), {
		query: {
			status: filters.status,
			monitor_id: filters.monitorId,
			cursor,
			limit,
		},
		signal,
	});
}

export function getIncident(
	workspaceId: string,
	incidentId: string,
	signal?: AbortSignal,
): Promise<IncidentDetail> {
	return apiRequest<IncidentDetail>(incidentPath(workspaceId, incidentId), {
		signal,
	});
}

export function acknowledgeIncident(
	workspaceId: string,
	incidentId: string,
): Promise<void> {
	return apiRequest<void>(
		`${incidentPath(workspaceId, incidentId)}/acknowledge`,
		{
			method: 'POST',
		},
	);
}

export async function listIncidentDeliveries(
	workspaceId: string,
	incidentId: string,
	signal?: AbortSignal,
): Promise<IncidentDelivery[]> {
	const response = await apiRequest<IncidentDeliveryListResponse>(
		`${incidentPath(workspaceId, incidentId)}/deliveries`,
		{ signal },
	);
	return response.deliveries;
}

export function getIncidentInsight(
	workspaceId: string,
	incidentId: string,
	signal?: AbortSignal,
): Promise<IncidentInsight> {
	return apiRequest<IncidentInsight>(
		`${incidentPath(workspaceId, incidentId)}/insights/latest`,
		{ signal },
	);
}

export function generateIncidentInsight(
	workspaceId: string,
	incidentId: string,
	force = false,
): Promise<IncidentInsight> {
	return apiRequest<IncidentInsight>(
		`${incidentPath(workspaceId, incidentId)}/insights`,
		{ method: 'POST', query: force ? { force: 'true' } : undefined },
	);
}
