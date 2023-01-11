#ifndef WORLD_H
#define WORLD_H

#define _FILE_OFFSET_BITS 64

#include "tools.h"
#include "engine.h"
#include "texture.h"
#include "state.h"

void freeocta(cube *c);
cube *loadchildren_buf(void *p, size_t len, int size, int _mapversion);
size_t savec_buf(void *p, unsigned int len, cube *c, int size);

MapState *partial_load_world(
        void *p,
        size_t len,
        int numvslots,
        int _worldsize,
        int _mapversion,
        int numlightmaps,
        int numpvs,
        int blendmap
);

int getnumvslots(MapState *state);
VSlot *getvslotindex(MapState *state, int i);

cube *getcubeindex(cube *c, int i);
void cube_setedge(cube *c, int i, uchar value);
void cube_settexture(cube *c, int i, ushort value);
cube *apply_messages(cube *c, int _worldsize, void *data, size_t len);

#endif
