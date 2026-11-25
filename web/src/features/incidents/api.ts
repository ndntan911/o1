import { ApiError, apiRequest } from '@/lib/api/client';
import type { ApiProblem } from '@/lib/api/problems';
import type {
	IncidentInsight,
	IncidentDelivery,
	IncidentDeliveryListResponse,
	IncidentDetail,
	IncidentFilters,
	IncidentInvestigation,
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

function investigationsPath(workspaceId: string, incidentId: string): string {
	return `${incidentPath(workspaceId, incidentId)}/investigations`;
}

export function getIncidentInvestigationLatest(
	workspaceId: string,
	incidentId: string,
	signal?: AbortSignal,
): Promise<IncidentInvestigation> {
	return apiRequest<IncidentInvestigation>(
		`${investigationsPath(workspaceId, incidentId)}/latest`,
		{ signal },
	);
}

export function getIncidentInvestigation(
	workspaceId: string,
	incidentId: string,
	investigationId: string,
	signal?: AbortSignal,
): Promise<IncidentInvestigation> {
	return apiRequest<IncidentInvestigation>(
		`${investigationsPath(workspaceId, incidentId)}/${investigationId}`,
		{ signal },
	);
}

export async function startIncidentInvestigation(
	workspaceId: string,
	incidentId: string,
): Promise<IncidentInvestigation> {
	const body = await apiRequest<IncidentInvestigation | ApiProblem | undefined>(
		investigationsPath(workspaceId, incidentId),
		{
			method: 'POST',
			// A 409 can still carry the run already in flight, so keep polling
			// it instead of treating the click as a failure.
			successStatuses: [409],
		},
	);
	if (!isInvestigation(body)) {
		// This 409 was a problem (or an empty body), not a row: no run is in
		// flight.
		throw new ApiError({
			type: body?.type ?? 'about:blank',
			title: body?.title ?? 'Conflict',
			status: body?.status ?? 409,
			code: body?.code ?? 'conflict',
			...(body?.detail ? { detail: body.detail } : {}),
		});
	}
	return body;
}

function isInvestigation(value: unknown): value is IncidentInvestigation {
	if (typeof value !== 'object' || value === null) {
		return false;
	}
	const candidate = value as Partial<IncidentInvestigation>;
	return (
		typeof candidate.id === 'string' && typeof candidate.status === 'string'
	);
}
