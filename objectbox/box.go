/*
 * Copyright 2018 ObjectBox Ltd. All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package objectbox

/*
#cgo LDFLAGS: -lobjectbox
#include <stdlib.h>
#include "objectbox.h"
*/
import "C"

import (
	"reflect"
	"sync/atomic"
	"unsafe"

	"github.com/google/flatbuffers/go"
)

// Box provides CRUD access to objects of a common type
type Box struct {
	objectBox *ObjectBox
	box       *C.OBX_box
	typeId    TypeId
	binding   ObjectBinding

	// Must be used in combination with fbbInUseAtomic
	fbb *flatbuffers.Builder

	// Values 0 (fbb available) or 1 (fbb in use); use only with CompareAndSwapInt32
	fbbInUseAtomic uint32
}

// Close fully closes the the Box connection and free's resources
func (box *Box) Close() (err error) {
	rc := C.obx_box_close(box.box)
	box.box = nil
	if rc != 0 {
		err = createError()
	}
	return
}

func (box *Box) idForPut(idCandidate uint64) (id uint64, err error) {
	id = uint64(C.obx_box_id_for_put(box.box, C.obx_id(idCandidate)))
	if id == 0 {
		err = createError()
	}
	return
}

// Puts the given object asynchronously (using another, internal, thread) for better performance.
//
// There are two main use cases:
//
// 1) "Put & Forget:" you gain faster puts as you don't have to wait for the transaction to finish.
//
// 2) Many small transactions: if your write load is typically a lot of individual puts that happen in parallel,
// this will merge small transactions into bigger ones. This results in a significant gain in overall throughput.
//
//
// In situations with (extremely) high async load, this method may be throttled (~1ms) or delayed (<1s).
// In the unlikely event that the object could not be enqueued after delaying, an error will be returned.
//
// Note that this method does not give you hard durability guarantees like the synchronous Put provides.
// There is a small time window (typically 3 ms) in which the data may not have been committed durably yet.
func (box *Box) PutAsync(object interface{}) (id uint64, err error) {
	idFromObject, err := box.binding.GetId(object)
	if err != nil {
		return
	}
	checkForPreviousValue := idFromObject != 0
	id, err = box.idForPut(idFromObject)
	if err != nil {
		return
	}

	var fbb *flatbuffers.Builder
	if atomic.CompareAndSwapUint32(&box.fbbInUseAtomic, 0, 1) {
		defer atomic.StoreUint32(&box.fbbInUseAtomic, 0)
		fbb = box.fbb
	} else {
		fbb = flatbuffers.NewBuilder(256)
	}
	box.binding.Flatten(object, fbb, id)
	return id, box.finishFbbAndPutAsync(fbb, id, checkForPreviousValue)
}

func (box *Box) finishFbbAndPutAsync(fbb *flatbuffers.Builder, id uint64, checkForPreviousObject bool) (err error) {
	fbb.Finish(fbb.EndObject())
	bytes := fbb.FinishedBytes()

	rc := C.obx_box_put_async(box.box,
		C.obx_id(id), unsafe.Pointer(&bytes[0]), C.size_t(len(bytes)), C.bool(checkForPreviousObject))
	if rc != 0 {
		err = createError()
	}

	// Reset to have a clear state for the next caller
	fbb.Reset()

	return
}

// Put synchronously inserts/updates a single object
// in case the ID is not given, it would be assigned automatically
func (box *Box) Put(object interface{}) (id uint64, err error) {
	err = box.objectBox.runWithCursor(box.typeId, false, func(cursor *cursor) error {
		var errInner error
		id, errInner = cursor.Put(object)
		return errInner
	})
	return
}

// The given argument must be a slice of the object type this Box represents (pointers to objects)
// in case IDs are not set on the objects, they would be assigned automatically
// Returns: IDs of the put objects (in the same order).
// Note: The slice may be empty or even nil; in both cases, an empty IDs slice and no error is returned.
func (box *Box) PutAll(slice interface{}) (ids []uint64, err error) {
	if slice == nil {
		return []uint64{}, nil
	}
	// TODO Check if reflect is fast; we could go via ObjectBinding and concrete types otherwise
	sliceValue := reflect.ValueOf(slice)
	count := sliceValue.Len()
	if count == 0 {
		return []uint64{}, nil
	}
	err = box.objectBox.runWithCursor(box.typeId, false, func(cursor *cursor) error {
		ids = make([]uint64, count)
		for i := 0; i < count; i++ {
			id, errPut := cursor.Put(sliceValue.Index(i).Interface())
			if errPut != nil {
				return errPut
			}
			ids[i] = id
		}
		return nil
	})
	return
}

// Remove deletes a single object
func (box *Box) Remove(id uint64) (err error) {
	return box.objectBox.runWithCursor(box.typeId, false, func(cursor *cursor) error {
		return cursor.Remove(id)
	})
}

// RemoveAll removes all stored objects
// it's much faster than removing objects one by one
func (box *Box) RemoveAll() (err error) {
	return box.objectBox.runWithCursor(box.typeId, false, func(cursor *cursor) error {
		return cursor.RemoveAll()
	})
}

// Count returns a number of objects stored
func (box *Box) Count() (count uint64, err error) {
	err = box.objectBox.runWithCursor(box.typeId, true, func(cursor *cursor) error {
		var errInner error
		count, errInner = cursor.Count()
		return errInner
	})
	return
}

// Get reads a single object
// it returns an interface that should be cast to the appropriate type
// the cast is done automatically when using the generated BoxFor* code
func (box *Box) Get(id uint64) (object interface{}, err error) {
	err = box.objectBox.runWithCursor(box.typeId, true, func(cursor *cursor) error {
		var errInner error
		object, errInner = cursor.Get(id)
		return errInner
	})
	return
}

// Get reads a all stored objects
// it returns a slice of objects that should be cast to the appropriate type
// the cast is done automatically when using the generated BoxFor* code
func (box *Box) GetAll() (slice interface{}, err error) {
	err = box.objectBox.runWithCursor(box.typeId, true, func(cursor *cursor) error {
		var errInner error
		slice, errInner = cursor.GetAll()
		return errInner
	})
	return
}
