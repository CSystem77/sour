import * as R from 'ramda'
import type {
  Asset,
  AssetData,
  AssetDataResponse,
  AssetIndex,
  LoadState,
  AssetRequest,
  AssetResponse,
  AssetSource,
  AssetState,
  AssetStateResponse,
  Bundle,
  BundleData,
  GameMap,
  GameMod,
  IndexAsset,
  IndexResponse,
  MountData,
} from './types'
import {
  ResponseType,
  RequestType,
  LoadStateType,
  DataType,
  LoadRequestType,
  load,
} from './types'
import type { DownloadState } from '../types'

import { getBlob as getSavedBlob, saveBlob, haveBlob } from './storage'

class PullError extends Error {}

let assetSources: string[] = []
let assetIndex: Maybe<AssetIndex> = null

async function fetchIndex(source: string): Promise<AssetSource> {
  const response = await fetch(source)
  const index: AssetSource = await response.json()
  index.source = source
  return index
}

async function fetchIndices(): Promise<AssetIndex> {
  const indices = await Promise.all(R.map((v) => fetchIndex(v), assetSources))
  return indices
}

function cleanPath(source: string): string {
  const lastSlash = source.lastIndexOf('/')
  if (lastSlash === -1) {
    return ''
  }

  return source.slice(0, lastSlash + 1)
}

const INT_SIZE = 4
function unpackBundle(data: ArrayBuffer): BundleData {
  const view = new DataView(data)

  let offset = 0

  const pathLength = view.getInt32(offset)
  offset += INT_SIZE
  const paths = JSON.parse(
    new TextDecoder().decode(new Uint8Array(data, offset, pathLength))
  )
  offset += pathLength

  const metadataLength = view.getInt32(offset)
  offset += INT_SIZE
  const metadata = JSON.parse(
    new TextDecoder().decode(new Uint8Array(data, offset, metadataLength))
  )
  offset += metadataLength

  return {
    dataOffset: offset,
    buffer: data,
    size: metadata.remote_package_size,
    directories: paths,
    files: metadata.files,
  }
}

async function fetchData(
  source: string,
  id: string,
  progress: (bundle: AssetLoadState) => void
): Promise<ArrayBuffer> {
  const request = new XMLHttpRequest()
  const packageName = `${cleanPath(source)}${id}`
  request.open('GET', packageName, true)
  request.responseType = 'arraybuffer'
  request.onprogress = (event) => {
    if (!event.lengthComputable) return
    progress(
      load.downloading({
        downloadedBytes: event.loaded,
        totalBytes: event.total,
      })
    )
  }
  request.onerror = function (event) {
    throw new PullError('NetworkError for: ' + packageName)
  }

  return new Promise((resolve, reject) => {
    request.onload = function (event) {
      if (
        request.status == 200 ||
        request.status == 304 ||
        request.status == 206 ||
        (request.status == 0 && request.response)
      ) {
        resolve(request.response)
      } else {
        throw new PullError(request.statusText + ' : ' + request.responseURL)
      }
    }
    request.send(null)
  })
}

async function loadBlob(
  source: string,
  id: string,
  url: string,
  progress: (bundle: AssetLoadState) => void
): Promise<ArrayBuffer> {
  if (await haveBlob(id)) {
    const buffer = await getSavedBlob(id)
    if (buffer == null) {
      throw new PullError(`Asset ${id} did not exist`)
    }
    return buffer
  }

  const buffer = await fetchData(source, url, progress)
  await saveBlob(id, buffer)
  return buffer
}

async function loadAsset(
  source: string,
  asset: Asset,
  progress: (bundle: AssetLoadState) => void
): Promise<MountData> {
  const { id, path } = asset

  const buffer = await loadBlob(source, id, id, progress)

  return {
    files: [
      {
        path,
        data: new Uint8Array(buffer),
      },
    ],
    buffers: [buffer],
  }
}

async function loadBundle(
  source: string,
  bundle: string,
  progress: (bundle: AssetLoadState) => void
): Promise<MountData> {
  const buffer = await loadBlob(source, bundle, `${bundle}.sour`, progress)
  const data = unpackBundle(buffer)

  return {
    files: R.map(
      ({ filename, start, end }): AssetData => ({
        path: filename,
        data: new Uint8Array(buffer, data.dataOffset + start, end - start),
      }),
      data.files
    ),
    buffers: [buffer],
  }
}

const resolveBundles = (source: AssetSource, bundles: string[]): Bundle[] =>
  R.chain((id) => {
    const bundle = R.find((v) => v.id === id, source.bundles)
    if (bundle == null) return []
    return [bundle]
  }, bundles)

const resolveAssets = (source: AssetSource, assets: IndexAsset[]): Asset[] =>
  R.chain(([id, path]) => {
    return [
      {
        id: source.assets[id],
        path,
      },
    ]
  }, assets)

type LookupResult = {
  assets: IndexAsset[]
  bundles: string[]
}

type ResolvedLookup = {
  source: string
  assets: Asset[]
  bundles: Bundle[]
}

const makeResolver =
  <T>(
    type: LoadRequestType,
    list: (source: AssetSource) => T[],
    matches: (target: string, item: T) => boolean,
    transform: (item: T) => LookupResult
  ) =>
  (source: AssetSource, target: string): Maybe<ResolvedLookup> => {
    const found = R.find((v) => matches(target, v), list(source))
    if (found == null) return null
    const { assets: foundAssets, bundles: foundBundles } = transform(found)

    // If anything inside of this result fails to resolve, it's game over,
    // the assets are missing.
    const assets = resolveAssets(source, foundAssets)
    if (assets.length !== foundAssets.length) return null
    const bundles = resolveBundles(source, foundBundles)
    if (bundles.length !== foundBundles.length) return null

    const { source: url } = source
    return {
      source: url,
      assets,
      bundles,
    }
  }

type Resolver = (source: AssetSource, target: string) => Maybe<ResolvedLookup>

const resolvers: Record<LoadRequestType, Resolver> = {
  [LoadRequestType.Map]: makeResolver<GameMap>(
    ({ maps }) => maps,
    (target, map) => map.name === target || map.id === target,
    ({ bundle, assets }) => {
      if (bundle != null) {
        return {
          bundles: [bundle],
          assets: [],
        }
      }

      return {
        bundles: [],
        assets,
      }
    }
  ),
  [LoadRequestType.Model]: makeResolver<Model>(
    ({ models }) => models,
    (target, model) => model.name === target || model.id === target,
    ({ id }) => {
      return {
        bundles: [id],
        assets: [],
      }
    }
  ),
  [LoadRequestType.Mod]: makeResolver<GameMod>(
    ({ mods }) => mods,
    (target, mod) => mod.name === target || mod.id === target,
    (mod) => {
      return {
        bundles: [mod.id],
        assets: [],
      }
    }
  ),
}

function resolveRequest(
  type: LoadRequestType,
  target: string
): Maybe<ResolvedLookup> {
  if (assetIndex == null) return null

  const resolver = resolvers[type]
  if (resolver == null) return null
  for (const source of assetIndex) {
    const resolved = resolver(source, target)
    if (resolved == null) continue
    return resolved
  }

  return null
}

const haveType =
  (type: LoadStateType) =>
  (states: AssetState[]): boolean =>
    R.any(({ state }) => state.type === type, states)

const haveWaiting = haveType(LoadStateType.Waiting)
const haveMissing = haveType(LoadStateType.Missing)
const haveFailed = haveType(LoadStateType.Failed)

const aggregateState = (states: AssetState[]): LoadState => {
  if (states.length === 0 || haveWaiting(states)) {
    return load.waiting()
  }

  // If we have any missing or errors, it's done.
  if (haveMissing(states)) {
    return load.missing()
  }

  if (haveFailed(states)) {
    return load.failed()
  }

  // Now all are either downloading or OK (we have no waiting, missing, or
  // failed)
  const downloadState: DownloadState = R.reduce(
    (a: DownloadState, state: AssetState): DownloadState => {
      const individual: DownloadState =
        state.type === LoadStateType.Downloading
          ? {
              downloadedBytes: state.downloadedBytes,
              totalBytes: state.totalBytes,
            }
          : state.type === LoadStateType.Ok
          ? {
              downloadedBytes: state.totalBytes,
              totalBytes: state.totalBytes,
            }
          : {
              downloadedBytes: 0,
              totalBytes: 0,
            }

      return {
        downloadedBytes: a.downloadedBytes + individual.downloadedBytes,
        totalBytes: a.totalBytes + individual.downloadedBytes,
      }
    },
    {
      downloadedBytes: 0,
      totalBytes: 0,
    },
    states
  )

  if (R.all(({ state }) => state.type === LoadStateType.Ok, states)) {
    return load.ok(downloadState.totalBytes)
  }

  return load.downloading(downloadState)
}

async function processLoad(
  pullId: string,
  type: LoadRequestType,
  target: string
) {
  let state: AssetState[] = []

  const setState = (newState: AssetState[]) => {
    const response: AssetStateResponse = {
      op: ResponseType.State,
      id: pullId,
      overall: aggregateState(newState),
      individual: newState,
    }
    self.postMessage(response)
    state = newState
  }
}

async function processLoad(
  pullId: string,
  type: LoadRequestType,
  target: string
) {
  if (assetIndex == null) {
    assetIndex = await fetchIndices()
  }

  const found = resolveRequest(type, target)

  if (found == null) {
    throw new Error(`Could not resolve ${LoadRequestType[type]} ${target}`)
  }

  const { source, bundles, assets } = found

  let state: AssetState[] = [
    ...R.map(
      ({ id }): AssetState => ({
        type: DataType.Bundle,
        state: {
          type: AssetLoadStateType.Waiting,
        },
        id,
      }),
      bundles
    ),
    ...R.map(
      ({ id }): AssetState => ({
        type: DataType.Asset,
        state: {
          type: AssetLoadStateType.Waiting,
        },
        id,
      }),
      assets
    ),
  ]

  const update =
    (type: DataType, id: string) => (loadState: AssetLoadState) => {
      state = R.map((item) => {
        if (item.type !== type || item.id !== id) return item
        return {
          ...item,
          state: loadState,
        }
      }, state)

      const response: AssetStateResponse = {
        op: ResponseType.State,
        // TODO
        status: AssetLoadStateType.Waiting,
        id: pullId,
        state,
      }

      self.postMessage(response)
    }

  try {
    const data: MountData[] = await Promise.all([
      ...R.map(
        ({ id }) => loadBundle(source, id, update(DataType.Bundle, id)),
        bundles
      ),
      ...R.map(
        (asset) => loadAsset(source, asset, update(DataType.Asset, asset.id)),
        assets
      ),
    ])

    const aggregated: MountData = R.reduce(
      (oldData: MountData, newData: MountData) => ({
        files: [...oldData.files, ...newData.files],
        buffers: [...oldData.buffers, ...newData.buffers],
      }),
      {
        files: [],
        buffers: [],
      },
      data
    )

    const response: AssetDataResponse = {
      op: ResponseType.Data,
      id: pullId,
      data: aggregated.files,
    }

    self.postMessage(response, aggregated.buffers)
  } catch (e) {
    if (!(e instanceof PullError)) throw e
  }
}

self.onmessage = (evt) => {
  const { data } = evt
  const request: AssetRequest = data

  if (request.op === RequestType.Environment) {
    const { assetSources: newSources } = request
    assetSources = newSources
    ;(async () => {
      assetIndex = await fetchIndices()

      const response: IndexResponse = {
        op: ResponseType.Index,
        index: assetIndex,
      }

      self.postMessage(response, [])
    })()
  } else if (request.op === RequestType.Load) {
    const { target, type, id } = request
    processLoad(id, type, target)
  }
}
