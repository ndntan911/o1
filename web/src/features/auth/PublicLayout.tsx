import { Outlet } from '@tanstack/react-router';
import { Card } from '@/components/ui/card';

export function PublicLayout() {
	return (
		<div className="flex min-h-screen flex-col items-center justify-center gap-6 bg-muted/30 p-4">
			<a
				href="/"
				className="text-sm font-medium tracking-tight text-muted-foreground hover:text-foreground"
			>
				o1 uptime
			</a>
			<Card className="w-full max-w-md">
				<Outlet />
			</Card>
		</div>
	);
}
