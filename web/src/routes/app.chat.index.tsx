import { createFileRoute } from '@tanstack/react-router';
import { ConversationPage } from '../pages/conversations';

export const Route = createFileRoute('/app/chat/')({ component: ConversationPage });
