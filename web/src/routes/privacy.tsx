import { createFileRoute } from '@tanstack/react-router';
import { Privacy } from '../pages/public';

export const Route = createFileRoute('/privacy')({ component: Privacy });
