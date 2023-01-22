package assets

import (
	"context"
	"strings"
)

type FetchResult struct {
	Data []byte
	Err  error
}

type FetchJob struct {
	Asset  string
	Result chan FetchResult
}

type AssetFetcher struct {
	jobs  chan FetchJob
	roots []*RemoteRoot
	cache Cache
}

func NewAssetFetcher(cache Cache, roots []string) (*AssetFetcher, error) {
	loaded, err := LoadRoots(cache, roots)
	if err != nil {
		return nil, err
	}

	remotes := make([]*RemoteRoot, 0)
	for _, root := range loaded {
		if remote, ok := root.(*RemoteRoot); ok {
			remotes = append(remotes, remote)
		}
	}

	return &AssetFetcher{
		roots: remotes,
		jobs:  make(chan FetchJob),
		cache: cache,
	}, nil
}

func (m *AssetFetcher) getAsset(ctx context.Context, id string) ([]byte, error) {
	for _, root := range m.roots {
		data, err := root.ReadAsset(id)
		if err == Missing {
			continue
		}
		return data, err
	}

	return nil, Missing
}

func (m *AssetFetcher) PollDownloads(ctx context.Context) {
	for {
		select {
		case job := <-m.jobs:
			data, err := m.getAsset(ctx, job.Asset)
			job.Result <- FetchResult{
				Data: data,
				Err:  err,
			}
		case <-ctx.Done():
			return
		}
	}
}

func (m *AssetFetcher) fetchAsset(ctx context.Context, id string) ([]byte, error) {
	out := make(chan FetchResult)
	m.jobs <- FetchJob{
		Asset:  id,
		Result: out,
	}

	result := <-out
	return result.Data, result.Err
}

type FoundMap struct {
	Map  *GameMap
	Root *RemoteRoot
}

func (m *AssetFetcher) FindMap(needle string) *FoundMap {
	otherTarget := needle + ".ogz"
	for _, root := range m.roots {
		for _, gameMap := range root.index.Maps {
			if gameMap.Name != needle && gameMap.Name != otherTarget && !strings.HasPrefix(gameMap.Id, needle) {
				continue
			}

			return &FoundMap{
				Map:  &gameMap,
				Root: root,
			}
		}
	}

	return nil
}

func (m *AssetFetcher) FetchMapBytes(ctx context.Context, needle string) ([]byte, error) {
	map_ := m.FindMap(needle)

	if map_ == nil {
		return nil, Missing
	}

	id, err := map_.Root.GetID(map_.Map.Ogz)
	if err != nil {
	    return nil, err
	}
	return m.fetchAsset(ctx, id)
}
