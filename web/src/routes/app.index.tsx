import { createFileRoute } from '@tanstack/react-router';
import { App } from '../pages/workspace';

export const Route = createFileRoute('/app/')({ component: App });
