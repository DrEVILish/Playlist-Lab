import React from 'react';
import { describe, it, expect, beforeEach, jest } from '@jest/globals';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import '@testing-library/jest-dom';
import { TemplateList, type MixTemplate } from './TemplateList';
import { useApp } from '../contexts/AppContext';

// TemplateList reads its API client from AppContext (useApp), not from global fetch.
// Mock the context hook so the component can be rendered in isolation, without needing
// a real AppProvider/AuthProvider tree (which would make real network calls on mount).
jest.mock('../contexts/AppContext', () => ({
  useApp: jest.fn(),
}));

const mockUseApp = useApp as jest.Mock;

const mockTemplates: MixTemplate[] = [
  {
    id: 1,
    name: 'Chill Evening Mix',
    description: 'Relaxing tracks for evening',
    mixType: 'mood',
    configuration: {
      mixType: 'mood',
      trackCount: 50,
      moods: ['chill', 'relaxing'],
    },
    createdAt: Date.now() / 1000,
    updatedAt: Date.now() / 1000,
    useCount: 5,
  },
  {
    id: 2,
    name: 'Rock Classics',
    description: 'Best rock songs',
    mixType: 'genre',
    configuration: {
      mixType: 'genre',
      trackCount: 100,
      genres: ['rock'],
    },
    createdAt: Date.now() / 1000 - 86400,
    updatedAt: Date.now() / 1000 - 86400,
    useCount: 10,
  },
  {
    id: 3,
    name: 'Beatles Collection',
    description: 'All Beatles tracks',
    mixType: 'artist',
    configuration: {
      mixType: 'artist',
      trackCount: 200,
      artistIds: ['123'],
    },
    createdAt: Date.now() / 1000 - 172800,
    updatedAt: Date.now() / 1000 - 172800,
    useCount: 3,
  },
];

const createMockApiClient = () => ({
  getMixTemplates: jest.fn(),
  deleteMixTemplate: jest.fn(),
});

describe('TemplateList - Rendering', () => {
  let mockApiClient: ReturnType<typeof createMockApiClient>;

  beforeEach(() => {
    jest.clearAllMocks();
    mockApiClient = createMockApiClient();
    mockApiClient.getMixTemplates.mockResolvedValue({ templates: mockTemplates });
    mockUseApp.mockReturnValue({ apiClient: mockApiClient });
  });

  it('should display all templates initially', async () => {
    render(<TemplateList />);

    await waitFor(() => {
      expect(screen.getByText('Chill Evening Mix')).toBeInTheDocument();
      expect(screen.getByText('Rock Classics')).toBeInTheDocument();
      expect(screen.getByText('Beatles Collection')).toBeInTheDocument();
    });
  });

  it('should show template descriptions and metadata', async () => {
    render(<TemplateList />);

    await waitFor(() => {
      expect(screen.getByText('Relaxing tracks for evening')).toBeInTheDocument();
      expect(screen.getByText('50 tracks')).toBeInTheDocument();
      expect(screen.getByText('100 tracks • 1 genres')).toBeInTheDocument();
      expect(screen.getByText('200 tracks • 1 artists')).toBeInTheDocument();
    });
  });

  it('should show use count for each template', async () => {
    render(<TemplateList />);

    await waitFor(() => {
      expect(screen.getByText('Used 5 times')).toBeInTheDocument();
      expect(screen.getByText('Used 10 times')).toBeInTheDocument();
      expect(screen.getByText('Used 3 times')).toBeInTheDocument();
    });
  });

  it('should show the total template count in the header', async () => {
    render(<TemplateList />);

    await waitFor(() => {
      expect(screen.getByLabelText('3 mixes')).toBeInTheDocument();
    });
  });

  it('should sort templates by most recently used/updated', async () => {
    render(<TemplateList />);

    await waitFor(() => {
      const cards = screen.getAllByRole('heading', { level: 3 });
      // mockTemplates are ordered from most-recent to least-recent updatedAt
      expect(cards[0]).toHaveTextContent('Chill Evening Mix');
      expect(cards[1]).toHaveTextContent('Rock Classics');
      expect(cards[2]).toHaveTextContent('Beatles Collection');
    });
  });
});

describe('TemplateList - Loading State', () => {
  let mockApiClient: ReturnType<typeof createMockApiClient>;

  beforeEach(() => {
    jest.clearAllMocks();
    mockApiClient = createMockApiClient();
    mockUseApp.mockReturnValue({ apiClient: mockApiClient });
  });

  it('should show loading spinner and message while fetching templates', async () => {
    let resolvePromise: (value: any) => void;
    const fetchPromise = new Promise((resolve) => {
      resolvePromise = resolve;
    });

    mockApiClient.getMixTemplates.mockReturnValue(fetchPromise);

    render(<TemplateList />);

    expect(screen.getByText('Loading templates...')).toBeInTheDocument();
    expect(document.querySelector('.loading-spinner')).toBeInTheDocument();

    resolvePromise!({ templates: mockTemplates });

    await waitFor(() => {
      expect(screen.queryByText('Loading templates...')).not.toBeInTheDocument();
      expect(screen.getByText('Chill Evening Mix')).toBeInTheDocument();
    });
  });
});

describe('TemplateList - Empty State', () => {
  let mockApiClient: ReturnType<typeof createMockApiClient>;

  beforeEach(() => {
    jest.clearAllMocks();
    mockApiClient = createMockApiClient();
    mockUseApp.mockReturnValue({ apiClient: mockApiClient });
  });

  it('should show empty state when no templates exist', async () => {
    mockApiClient.getMixTemplates.mockResolvedValue({ templates: [] });

    render(<TemplateList />);

    await waitFor(() => {
      expect(screen.getByText('No saved mixes yet')).toBeInTheDocument();
      expect(screen.getByText('Create a custom mix and save it to get started')).toBeInTheDocument();
    });
  });
});

describe('TemplateList - Error State', () => {
  let mockApiClient: ReturnType<typeof createMockApiClient>;

  beforeEach(() => {
    jest.clearAllMocks();
    mockApiClient = createMockApiClient();
    mockUseApp.mockReturnValue({ apiClient: mockApiClient });
  });

  it('should show an error message and retry button when loading fails', async () => {
    mockApiClient.getMixTemplates.mockRejectedValue(new Error('Failed to load templates'));

    render(<TemplateList />);

    await waitFor(() => {
      expect(screen.getByText('Failed to load templates')).toBeInTheDocument();
      expect(screen.getByRole('button', { name: /retry loading templates/i })).toBeInTheDocument();
    });
  });

  it('should retry loading templates when retry button is clicked', async () => {
    mockApiClient.getMixTemplates
      .mockRejectedValueOnce(new Error('Failed to load templates'))
      .mockResolvedValueOnce({ templates: mockTemplates });

    render(<TemplateList />);

    await waitFor(() => {
      expect(screen.getByText('Failed to load templates')).toBeInTheDocument();
    });

    fireEvent.click(screen.getByRole('button', { name: /retry loading templates/i }));

    await waitFor(() => {
      expect(screen.getByText('Chill Evening Mix')).toBeInTheDocument();
    });

    expect(mockApiClient.getMixTemplates).toHaveBeenCalledTimes(2);
  });
});

describe('TemplateList - Actions', () => {
  let mockApiClient: ReturnType<typeof createMockApiClient>;

  beforeEach(() => {
    jest.clearAllMocks();
    mockApiClient = createMockApiClient();
    mockApiClient.getMixTemplates.mockResolvedValue({ templates: mockTemplates });
    mockUseApp.mockReturnValue({ apiClient: mockApiClient });
  });

  it('should not show Generate/Schedule/Edit buttons when their callbacks are not provided', async () => {
    render(<TemplateList />);

    await waitFor(() => {
      expect(screen.getByText('Chill Evening Mix')).toBeInTheDocument();
    });

    expect(screen.queryByRole('button', { name: /generate mix/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /schedule.*mix/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /edit.*template/i })).not.toBeInTheDocument();
  });

  it('should call onGenerate with the template when Generate is clicked', async () => {
    const onGenerate = jest.fn();
    render(<TemplateList onGenerate={onGenerate} />);

    await waitFor(() => {
      expect(screen.getByText('Chill Evening Mix')).toBeInTheDocument();
    });

    fireEvent.click(screen.getByRole('button', { name: /generate mix from chill evening mix template/i }));

    await waitFor(() => {
      expect(onGenerate).toHaveBeenCalledWith(mockTemplates[0]);
    });
  });

  it('should call onSchedule with the template when Schedule is clicked', async () => {
    const onSchedule = jest.fn();
    render(<TemplateList onSchedule={onSchedule} />);

    await waitFor(() => {
      expect(screen.getByText('Chill Evening Mix')).toBeInTheDocument();
    });

    fireEvent.click(screen.getByRole('button', { name: /schedule chill evening mix mix/i }));

    await waitFor(() => {
      expect(onSchedule).toHaveBeenCalledWith(mockTemplates[0]);
    });
  });

  it('should call onEdit with the template when Edit is clicked', async () => {
    const onEdit = jest.fn();
    render(<TemplateList onEdit={onEdit} />);

    await waitFor(() => {
      expect(screen.getByText('Chill Evening Mix')).toBeInTheDocument();
    });

    fireEvent.click(screen.getByRole('button', { name: /edit chill evening mix template/i }));

    await waitFor(() => {
      expect(onEdit).toHaveBeenCalledWith(mockTemplates[0]);
    });
  });
});

describe('TemplateList - Delete Functionality', () => {
  let mockApiClient: ReturnType<typeof createMockApiClient>;

  beforeEach(() => {
    jest.clearAllMocks();
    mockApiClient = createMockApiClient();
    mockApiClient.getMixTemplates.mockResolvedValue({ templates: mockTemplates });
    mockUseApp.mockReturnValue({ apiClient: mockApiClient });
  });

  it('should open delete dialog when delete button is clicked', async () => {
    render(<TemplateList />);

    await waitFor(() => {
      expect(screen.getByText('Chill Evening Mix')).toBeInTheDocument();
    });

    const deleteButton = screen.getByRole('button', { name: /delete chill evening mix template/i });
    fireEvent.click(deleteButton);

    await waitFor(() => {
      expect(screen.getByText('Delete Template?')).toBeInTheDocument();
      expect(screen.getAllByText(/Chill Evening Mix/)[0]).toBeInTheDocument();
    });
  });

  it('should close delete dialog when cancel is clicked', async () => {
    render(<TemplateList />);

    await waitFor(() => {
      expect(screen.getByText('Chill Evening Mix')).toBeInTheDocument();
    });

    const deleteButtons = screen.getAllByRole('button', { name: /^delete/i });
    fireEvent.click(deleteButtons[0]);

    await waitFor(() => {
      expect(screen.getByText('Delete Template?')).toBeInTheDocument();
    });

    const cancelButton = screen.getByRole('button', { name: /cancel/i });
    fireEvent.click(cancelButton);

    await waitFor(() => {
      expect(screen.queryByText('Delete Template?')).not.toBeInTheDocument();
    });
  });

  it('should delete template when confirmed', async () => {
    mockApiClient.deleteMixTemplate.mockResolvedValue({ message: 'Template deleted successfully' });

    render(<TemplateList />);

    await waitFor(() => {
      expect(screen.getByText('Chill Evening Mix')).toBeInTheDocument();
    });

    const deleteButton = screen.getByRole('button', { name: /delete chill evening mix template/i });
    fireEvent.click(deleteButton);

    await waitFor(() => {
      expect(screen.getByText('Delete Template?')).toBeInTheDocument();
    });

    const confirmButton = screen.getByRole('button', { name: /confirm delete/i });
    fireEvent.click(confirmButton);

    await waitFor(() => {
      expect(mockApiClient.deleteMixTemplate).toHaveBeenCalledWith(1);
    });

    await waitFor(() => {
      expect(screen.queryByText('Chill Evening Mix')).not.toBeInTheDocument();
    });
  });

  it('should show success message after deletion', async () => {
    mockApiClient.deleteMixTemplate.mockResolvedValue({ message: 'Template deleted successfully' });

    render(<TemplateList />);

    await waitFor(() => {
      expect(screen.getByText('Chill Evening Mix')).toBeInTheDocument();
    });

    fireEvent.click(screen.getByRole('button', { name: /delete chill evening mix template/i }));

    await waitFor(() => {
      expect(screen.getByText('Delete Template?')).toBeInTheDocument();
    });

    fireEvent.click(screen.getByRole('button', { name: /confirm delete/i }));

    await waitFor(() => {
      expect(screen.getByText(/Saved mix "Chill Evening Mix" deleted successfully/)).toBeInTheDocument();
    });
  });

  it('should handle delete error gracefully', async () => {
    mockApiClient.deleteMixTemplate.mockRejectedValue(new Error('Failed to delete template. Please try again.'));

    render(<TemplateList />);

    await waitFor(() => {
      expect(screen.getByText('Chill Evening Mix')).toBeInTheDocument();
    });

    const deleteButton = screen.getByRole('button', { name: /delete chill evening mix template/i });
    fireEvent.click(deleteButton);

    await waitFor(() => {
      expect(screen.getByText('Delete Template?')).toBeInTheDocument();
    });

    const confirmButton = screen.getByRole('button', { name: /confirm delete/i });
    fireEvent.click(confirmButton);

    await waitFor(() => {
      expect(screen.getByText(/failed to delete template/i)).toBeInTheDocument();
    });
  });

  it('should disable buttons during deletion', async () => {
    let resolveDelete: (value: any) => void;
    const deletePromise = new Promise((resolve) => {
      resolveDelete = resolve;
    });
    mockApiClient.deleteMixTemplate.mockReturnValue(deletePromise);

    render(<TemplateList />);

    await waitFor(() => {
      expect(screen.getByText('Chill Evening Mix')).toBeInTheDocument();
    });

    const deleteButton = screen.getByRole('button', { name: /delete chill evening mix template/i });
    fireEvent.click(deleteButton);

    await waitFor(() => {
      expect(screen.getByText('Delete Template?')).toBeInTheDocument();
    });

    const confirmButton = screen.getByRole('button', { name: /confirm delete/i });
    fireEvent.click(confirmButton);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /deleting/i })).toBeDisabled();
      expect(screen.getByRole('button', { name: /cancel/i })).toBeDisabled();
    });

    resolveDelete!({ message: 'Template deleted successfully' });

    await waitFor(() => {
      expect(screen.queryByText('Delete Template?')).not.toBeInTheDocument();
    });
  });

  it('should not show delete button when there are no templates', async () => {
    mockApiClient.getMixTemplates.mockResolvedValue({ templates: [] });

    render(<TemplateList />);

    await waitFor(() => {
      expect(screen.getByText('No saved mixes yet')).toBeInTheDocument();
    });

    const deleteButtons = screen.queryAllByRole('button', { name: /delete/i });
    expect(deleteButtons).toHaveLength(0);
  });
});
