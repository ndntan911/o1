import { useState } from 'react';
import { EmptyState } from '@/components/feedback/EmptyState';
import { ErrorPanel } from '@/components/feedback/ErrorPanel';
import { PageSkeleton } from '@/components/feedback/Skeletons';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from '@/components/ui/table';
import { isApiProblem } from '@/lib/api/client';
import { problemFromError } from '@/lib/api/problem-from-error';
import type { Role, Workspace } from '@/lib/api/types';
import { assignableRoles } from '@/lib/roles';
import { AddMemberDialog, type AssignableRole } from './AddMemberDialog';
import { useMembers } from './hooks';

export interface MembersScreenProps {
	workspace: Workspace;
}

function isAssignableRole(role: Role): role is AssignableRole {
	return role !== 'owner';
}

function errorMessage(error: unknown): string {
	if (isApiProblem(error)) {
		return error.problem.detail ?? error.problem.title;
	}
	if (error instanceof Error) return error.message;
	return 'Please try again.';
}

export function MembersScreen({ workspace }: MembersScreenProps) {
	const members = useMembers(workspace.id);
	const [addOpen, setAddOpen] = useState(false);
	const roles = assignableRoles(workspace.role).filter(isAssignableRole);

	return (
		<section className="space-y-6">
			<div className="flex flex-wrap items-center justify-between gap-3">
				<div>
					<h1 className="font-heading text-xl font-semibold">Members</h1>
					<p className="text-sm text-muted-foreground">
						People with access to {workspace.name}.
					</p>
				</div>
				{roles.length > 0 ? (
					<Button type="button" onClick={() => setAddOpen(true)}>
						Add member
					</Button>
				) : null}
			</div>

			{members.isPending ? (
				<PageSkeleton />
			) : members.isError ? (
				<ErrorPanel
					message={errorMessage(members.error)}
					problem={problemFromError(members.error)}
					onRetry={() => void members.refetch()}
				/>
			) : members.data.length === 0 ? (
				<EmptyState
					title="No members yet"
					description="Members of this workspace will appear here."
				/>
			) : (
				<Table>
					<TableHeader>
						<TableRow>
							<TableHead>Email</TableHead>
							<TableHead>Role</TableHead>
						</TableRow>
					</TableHeader>
					<TableBody>
						{members.data.map((member) => (
							<TableRow key={member.user_id}>
								<TableCell>{member.email}</TableCell>
								<TableCell>
									<Badge variant="outline">{member.role}</Badge>
								</TableCell>
							</TableRow>
						))}
					</TableBody>
				</Table>
			)}

			<AddMemberDialog
				workspaceId={workspace.id}
				roles={roles}
				open={addOpen}
				onOpenChange={setAddOpen}
			/>
		</section>
	);
}
