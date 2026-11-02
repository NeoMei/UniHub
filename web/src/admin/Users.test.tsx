// @vitest-environment jsdom

import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, expect, test } from 'vitest';
import { ProductRole } from './Users';

afterEach(cleanup);

test('product access checkboxes use the admin checkbox control style', () => {
  render(
    <ProductRole
      user="user-1"
      manifest={{
        product_id: 'llmrouter',
        instance_id: 'default',
        name: 'LLMRouter',
        roles: [
          { id: 'operator', name: 'Channel operator', privileged: true },
          { id: 'member', name: 'Member' },
        ],
      }}
      assignment={{ roles: ['member'], active: true, assignment_version: 7 }}
      principal={{ user_id: 'admin', roles: ['super_admin'], permissions: ['*'], enabled: true, version: 1 }}
      onSaved={() => {}}
    />,
  );

  expect(screen.getAllByRole('checkbox').every((input) => input.classList.contains('admin-checkbox'))).toBe(true);
});
