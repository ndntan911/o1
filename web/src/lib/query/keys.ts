import type {
	AdminMonitorFilters,
	AdminWorkspaceFilters,
	IncidentFilters,
	ResultsWindow,
	SecurityEventFilters,
} from '@/lib/api/types';

export const queryKeys = {
	me: ['me'] as const,
	monitors: (workspaceId: string) => ['monitors', workspaceId] as const,
	monitor: (workspaceId: string, monitorId: string) =>
		['monitor', workspaceId, monitorId] as const,
	results: (workspaceId: string, monitorId: string, window: ResultsWindow) =>
		['results', workspaceId, monitorId, window] as const,
	summary: (workspaceId: string, monitorId: string, window: ResultsWindow) =>
		['summary', workspaceId, monitorId, window] as const,
	members: (workspaceId: string) => ['members', workspaceId] as const,
	adminWorkspaces: (filters: AdminWorkspaceFilters) =>
		['admin', 'workspaces', filters] as const,
	adminMonitors: (filters: AdminMonitorFilters) =>
		['admin', 'monitors', filters] as const,
	adminSecurityEvents: (filters: SecurityEventFilters) =>
		['admin', 'security-events', filters] as const,
	incidents: (workspaceId: string, filters: IncidentFilters) =>
		['incidents', workspaceId, filters] as const,
	incident: (workspaceId: string, incidentId: string) =>
		['incident', workspaceId, incidentId] as const,
	incidentDeliveries: (workspaceId: string, incidentId: string) =>
		['incident-deliveries', workspaceId, incidentId] as const,
	alertChannels: (workspaceId: string) =>
		['alert-channels', workspaceId] as const,
	monitorAlertChannel: (workspaceId: string, monitorId: string) =>
		['monitor-alert-channel', workspaceId, monitorId] as const,
	monitorAlertChannels: (workspaceId: string, monitorId: string) =>
		['monitor-alert-channels', workspaceId, monitorId] as const,
};
